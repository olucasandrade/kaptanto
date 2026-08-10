package enrich_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/enrich"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEvent(table string, op event.Operation) *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             ulid.MustParse("01J5Z0000000000000000000B1"),
		IdempotencyKey: "pg:public." + table + ":1:" + string(op) + ":0/1",
		Timestamp:      time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		Source:         "postgres://cdc@localhost:5432/shop",
		Operation:      op,
		Database:       "shop",
		Schema:         "public",
		Table:          table,
		Key:            json.RawMessage(`{"id":1}`),
		Before:         nil,
		After:          json.RawMessage(`{"id":1,"status":"pending"}`),
		Metadata:       map[string]any{"lsn": "0/1"},
	}
}

// enrichTestTimeout avoids flakes when the full suite runs many parallel
// httptest servers; production default remains 150ms (AIC-01).
const enrichTestTimeout = "10s"

// exactly16KiBJSONObject returns a valid JSON object of exactly maxAIContextBytes
// (AIC-02 upper bound). Guards off-by-one regressions that accept ≥ instead of >.
func exactly16KiBJSONObject(t *testing.T) []byte {
	t.Helper()
	const max = 16 * 1024
	prefix := []byte(`{"p":"`)
	suffix := []byte(`"}`)
	padLen := max - len(prefix) - len(suffix)
	require.Greater(t, padLen, 0)
	body := make([]byte, 0, max)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("x"), padLen)...)
	body = append(body, suffix...)
	require.Equal(t, max, len(body))
	require.True(t, json.Valid(body))
	return body
}

func mustCompile(t testing.TB, cfg config.EnrichmentConfig, m *observability.KaptantoMetrics) *enrich.Enricher {
	t.Helper()
	if cfg.Timeout == "" && strings.TrimSpace(cfg.URL) != "" && len(cfg.Tables) > 0 {
		cfg.Timeout = enrichTestTimeout
	}
	e, err := enrich.Compile(cfg, m)
	require.NoError(t, err)
	return e
}

func failureCount(m *observability.KaptantoMetrics, reason string) float64 {
	return testutil.ToFloat64(m.EnrichmentFailuresTotal.WithLabelValues(reason))
}

// --- Contract matrix (AIC-01/02) ---

func TestEnrich_ContractMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		handler        http.HandlerFunc
		wantAI         string // empty = nil AIContext
		wantExactBytes int    // when set, assert AIContext length (AIC-02 boundary)
		wantReason     string // empty = no failure
		slow           time.Duration
	}{
		{
			name: "200-valid",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.True(t, json.Valid(body))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"intent":"fulfill","custom":{"ok":true}}`))
			},
			wantAI: `{"intent":"fulfill","custom":{"ok":true}}`,
		},
		{
			name: "204-no-context",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			},
		},
		{
			name: "500-status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantReason: enrich.ReasonStatus,
		},
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(150 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"intent":"late"}`))
			},
			wantReason: enrich.ReasonTimeout,
			slow:       30 * time.Millisecond,
		},
		{
			name: "exactly-16kib-object",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(exactly16KiBJSONObject(t))
			},
			wantExactBytes: 16 * 1024,
		},
		{
			name: "oversize",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				// 16 KiB + 1
				_, _ = w.Write(bytes.Repeat([]byte("a"), 16*1024+1))
			},
			wantReason: enrich.ReasonOversize,
		},
		{
			name: "invalid-json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{not-json`))
			},
			wantReason: enrich.ReasonInvalid,
		},
		{
			name: "non-object-array",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[1,2,3]`))
			},
			wantReason: enrich.ReasonNonObject,
		},
		{
			name: "non-object-string",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`"hello"`))
			},
			wantReason: enrich.ReasonNonObject,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)

			timeout := enrichTestTimeout
			if tc.slow > 0 {
				timeout = tc.slow.String()
			}
			m := observability.NewKaptantoMetrics()
			e := mustCompile(t, config.EnrichmentConfig{
				URL:     srv.URL,
				Tables:  []string{"public.orders"},
				Timeout: timeout,
			}, m)

			ev := testEvent("orders", event.OpInsert)
			e.Enrich(context.Background(), ev)

			if tc.wantExactBytes > 0 {
				require.NotNil(t, ev.AIContext)
				assert.Equal(t, tc.wantExactBytes, len(ev.AIContext))
				assert.True(t, json.Valid(ev.AIContext))
			} else if tc.wantAI != "" {
				require.NotNil(t, ev.AIContext)
				assert.JSONEq(t, tc.wantAI, string(ev.AIContext))
			} else {
				assert.Nil(t, ev.AIContext)
			}
			if tc.wantReason != "" {
				assert.Equal(t, float64(1), failureCount(m, tc.wantReason),
					"expected one failure with reason %s", tc.wantReason)
			} else {
				for _, reason := range []string{
					enrich.ReasonTimeout, enrich.ReasonStatus, enrich.ReasonError,
					enrich.ReasonInvalid, enrich.ReasonOversize, enrich.ReasonNonObject,
				} {
					assert.Equal(t, float64(0), failureCount(m, reason), reason)
				}
			}
		})
	}
}

func TestEnrich_AuthTokenBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("ENRICH_TOKEN", "s3cret")
	e := mustCompile(t, config.EnrichmentConfig{
		URL:       srv.URL,
		Tables:    []string{"public.orders"},
		Timeout:   enrichTestTimeout,
		AuthToken: "${ENRICH_TOKEN}",
	}, nil)
	e.Enrich(context.Background(), testEvent("orders", event.OpInsert))
	assert.Equal(t, "Bearer s3cret", gotAuth)
}

func TestCompile_MissingAuthEnv(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:       "http://127.0.0.1:9/enrich",
		Tables:    []string{"public.orders"},
		AuthToken: "${MISSING_ENRICH_TOKEN_XYZ}",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING_ENRICH_TOKEN_XYZ")
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestCompile_LiteralAuthToken_Rejects(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:       "http://127.0.0.1:9/enrich",
		Tables:    []string{"public.orders"},
		AuthToken: "literal-secret",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${VAR}")
}

func TestCompile_BlocksMetadataIP(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:    "http://169.254.169.254/latest/meta-data/",
		Tables: []string{"public.orders"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestCompile_BlocksPrivateIPWithoutAllowlist(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:    "http://10.0.0.5/enrich",
		Tables: []string{"public.orders"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestCompile_AllowsAllowlistedSidecar(t *testing.T) {
	t.Parallel()
	e, err := enrich.Compile(config.EnrichmentConfig{
		URL:        "http://10.0.0.5/enrich",
		Tables:     []string{"public.orders"},
		AllowHosts: []string{"10.0.0.5"},
	}, nil)
	require.NoError(t, err)
	assert.True(t, e.Enabled())
}

func TestEnrich_CircuitBreaker_FastFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"late"}`))
	}))
	t.Cleanup(srv.Close)

	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: "15ms",
	}, m)
	enrich.SetCircuitThresholdForTest(e, 3)

	for i := 0; i < 3; i++ {
		ev := testEvent("orders", event.OpInsert)
		e.Enrich(context.Background(), ev)
	}

	start := time.Now()
	ev := testEvent("orders", event.OpInsert)
	e.Enrich(context.Background(), ev)
	elapsed := time.Since(start)

	assert.Nil(t, ev.AIContext)
	assert.Less(t, elapsed, 10*time.Millisecond, "circuit open must skip HTTP wait")
	assert.Equal(t, float64(3), failureCount(m, enrich.ReasonTimeout))
	assert.Equal(t, float64(1), failureCount(m, enrich.ReasonCircuitOpen))

	time.Sleep(120 * time.Millisecond)
}

func TestEnrich_CircuitBreaker_RecoversAfterCooldown(t *testing.T) {
	var fast atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fast.Load() {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"intent":"late"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: "15ms",
	}, m)
	enrich.SetCircuitThresholdForTest(e, 3)
	enrich.SetCircuitCooldownForTest(e, 20*time.Millisecond)

	for i := 0; i < 3; i++ {
		ev := testEvent("orders", event.OpInsert)
		e.Enrich(context.Background(), ev)
	}

	ev := testEvent("orders", event.OpInsert)
	e.Enrich(context.Background(), ev)
	assert.Nil(t, ev.AIContext)

	fast.Store(true)
	time.Sleep(25 * time.Millisecond)

	ev2 := testEvent("orders", event.OpInsert)
	e.Enrich(context.Background(), ev2)
	assert.NotNil(t, ev2.AIContext)
	assert.JSONEq(t, `{"intent":"ok"}`, string(ev2.AIContext))
}

func TestWrap_PropagatesCancelContext(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"blocked"}`))
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})

	inner := &memLog{}
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	el := enrich.Wrap(ctx, inner, e)

	ev := testEvent("orders", event.OpInsert)
	_, err := el.Append(ev)
	require.NoError(t, err)
	assert.Nil(t, ev.AIContext)
	assert.Len(t, inner.appended, 1)
}

// --- Match scoping ---

func TestEnrich_EmptyTables_Disabled(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	e := mustCompile(t, config.EnrichmentConfig{
		URL:    srv.URL,
		Tables: nil, // explicit opt-in required
	}, nil)
	assert.False(t, e.Enabled())
	e.Enrich(context.Background(), testEvent("orders", event.OpInsert))
	assert.Equal(t, int64(0), hits.Load())
}

func TestEnrich_EmptyURL_Disabled(t *testing.T) {
	t.Parallel()
	e := mustCompile(t, config.EnrichmentConfig{
		Tables: []string{"public.orders"},
	}, nil)
	assert.False(t, e.Enabled())
	ev := testEvent("orders", event.OpInsert)
	e.Enrich(context.Background(), ev)
	assert.Nil(t, ev.AIContext)
}

func TestEnrich_NonMatchingTable_SkipsHTTP(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"x"}`))
	}))
	t.Cleanup(srv.Close)

	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, nil)
	ev := testEvent("users", event.OpInsert)
	e.Enrich(context.Background(), ev)
	assert.Equal(t, int64(0), hits.Load())
	assert.Nil(t, ev.AIContext)
}

func TestEnrich_DefaultOperations_SkipsDelete(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
		// Operations empty → insert,update only
	}, nil)
	e.Enrich(context.Background(), testEvent("orders", event.OpDelete))
	assert.Equal(t, int64(0), hits.Load())
	e.Enrich(context.Background(), testEvent("orders", event.OpInsert))
	assert.Equal(t, int64(1), hits.Load())
}

// --- Timeout-storm soak (AIC-01) ---

func TestEnrich_TimeoutStorm_FailOpen(t *testing.T) {
	// Sequential: timeout storm must not race other HTTP cases for CPU.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"late"}`))
	}))
	t.Cleanup(srv.Close)

	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: "15ms",
	}, m)
	enrich.SetCircuitThresholdForTest(e, 100) // isolate storm from circuit breaker

	var warnCount atomic.Int64
	enrich.SetWarnForTest(e, func(string, ...any) { warnCount.Add(1) })
	enrich.SetWarnEveryForTest(e, time.Hour) // one warn for the whole storm

	const n = 15
	for i := 0; i < n; i++ {
		ev := testEvent("orders", event.OpInsert)
		e.Enrich(context.Background(), ev)
		assert.Nil(t, ev.AIContext, "event %d must append unenriched", i)
	}

	assert.Equal(t, float64(n), failureCount(m, enrich.ReasonTimeout))
	assert.Equal(t, int64(1), warnCount.Load(), "timeout storm must rate-limit to one warn")

	// Let in-flight Sleep handlers finish before httptest.Close waits on them.
	time.Sleep(120 * time.Millisecond)
}

// --- Call-site / Wrap integration ---

func TestWrap_AppendEnrichesBeforeDurableWrite(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"from_enricher"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, observability.NewKaptantoMetrics())
	el := enrich.Wrap(context.Background(), inner, e)

	// Optional interfaces preserved for Badger.
	_, okNotify := el.(eventlog.PartitionNotifier)
	_, okObs := el.(eventlog.AppendObservable)
	assert.True(t, okNotify, "wrapped Badger must remain PartitionNotifier")
	assert.True(t, okObs, "wrapped Badger must remain AppendObservable")

	ev := testEvent("orders", event.OpInsert)
	seq, err := el.Append(ev)
	require.NoError(t, err)
	require.NotZero(t, seq)
	require.NotNil(t, ev.AIContext)
	assert.JSONEq(t, `{"intent":"from_enricher"}`, string(ev.AIContext))

	// Find our event across partitions.
	var found *event.ChangeEvent
	for p := uint32(0); p < 64; p++ {
		ents, err := el.ReadPartition(context.Background(), p, 1, 10)
		require.NoError(t, err)
		for i := range ents {
			if ents[i].Event.IdempotencyKey == ev.IdempotencyKey {
				found = ents[i].Event
			}
		}
	}
	require.NotNil(t, found, "enriched event must land in event log")
	require.NotNil(t, found.AIContext)
	assert.JSONEq(t, `{"intent":"from_enricher"}`, string(found.AIContext))

	// Exercise NotifyCh / RegisterObserver / Close forwarding on enrichingFull.
	ch := el.(eventlog.PartitionNotifier).NotifyCh(0)
	require.NotNil(t, ch)
	unreg := el.(eventlog.AppendObservable).RegisterObserver(&nopObserver{})
	unreg()
	require.NoError(t, el.Close())
}

type nopObserver struct{}

func (*nopObserver) ObserveAppend([]*event.ChangeEvent, []uint64) {}

func TestWrap_AppendBatchSerialEnrich(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"intent":"batch"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, nil)
	el := enrich.Wrap(context.Background(), inner, e)

	evs := []*event.ChangeEvent{
		testEvent("orders", event.OpInsert),
		testEvent("orders", event.OpUpdate),
	}
	evs[1].IdempotencyKey = "pg:public.orders:2:update:0/2"
	evs[1].ID = ulid.MustParse("01J5Z0000000000000000000B2")

	seqs, err := el.AppendBatch(evs)
	require.NoError(t, err)
	require.Len(t, seqs, 2)
	assert.Equal(t, int64(2), hits.Load(), "enrichment is serial per event in the batch")
	for _, ev := range evs {
		require.NotNil(t, ev.AIContext)
	}
}

// memLog is a minimal EventLog for Wrap branch coverage (no optional ifaces).
type memLog struct {
	appended []*event.ChangeEvent
}

func (m *memLog) Append(ev *event.ChangeEvent) (uint64, error) {
	m.appended = append(m.appended, ev)
	return uint64(len(m.appended)), nil
}
func (m *memLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	seqs := make([]uint64, len(evs))
	for i, ev := range evs {
		seq, err := m.Append(ev)
		if err != nil {
			return nil, err
		}
		seqs[i] = seq
	}
	return seqs, nil
}
func (m *memLog) ReadPartition(context.Context, uint32, uint64, int) ([]eventlog.LogEntry, error) {
	return nil, nil
}
func (m *memLog) Close() error { return nil }

type notifyOnlyLog struct {
	memLog
	ch chan struct{}
}

func (n *notifyOnlyLog) NotifyCh(uint32) <-chan struct{} { return n.ch }

type obsOnlyLog struct {
	memLog
}

func (o *obsOnlyLog) RegisterObserver(eventlog.AppendObserver) func() {
	return func() {}
}

func TestWrap_InterfaceBranches(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"*"},
		Timeout: enrichTestTimeout,
	}, nil)

	plain := enrich.Wrap(context.Background(), &memLog{}, e)
	_, hasN := plain.(eventlog.PartitionNotifier)
	_, hasO := plain.(eventlog.AppendObservable)
	assert.False(t, hasN)
	assert.False(t, hasO)
	require.NoError(t, plain.Close())

	nOnly := enrich.Wrap(context.Background(), &notifyOnlyLog{ch: make(chan struct{})}, e)
	require.NotNil(t, nOnly.(eventlog.PartitionNotifier).NotifyCh(1))
	_, hasO = nOnly.(eventlog.AppendObservable)
	assert.False(t, hasO)

	oOnly := enrich.Wrap(context.Background(), &obsOnlyLog{}, e)
	unreg := oOnly.(eventlog.AppendObservable).RegisterObserver(&nopObserver{})
	unreg()
	_, hasN = oOnly.(eventlog.PartitionNotifier)
	assert.False(t, hasN)
}

func TestEnrich_ConnectError_FailOpen(t *testing.T) {
	t.Parallel()
	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     "http://127.0.0.1:1/", // refused
		Tables:  []string{"public.orders"},
		Timeout: "100ms",
	}, m)
	ev := testEvent("orders", event.OpInsert)
	e.Enrich(context.Background(), ev)
	assert.Nil(t, ev.AIContext)
	assert.Equal(t, float64(1), failureCount(m, enrich.ReasonError)+failureCount(m, enrich.ReasonTimeout))
}

func TestEnrich_EmptyBody200_Invalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, m)
	e.Enrich(context.Background(), testEvent("orders", event.OpInsert))
	assert.Equal(t, float64(1), failureCount(m, enrich.ReasonInvalid))
}

func TestCompile_UnsupportedScheme(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:    "ftp://example.com/x",
		Tables: []string{"public.orders"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}

func TestCompile_InvalidTableGlob(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:    "http://127.0.0.1:9/",
		Tables: []string{"public.*.*"},
	}, nil)
	require.Error(t, err)
}

func TestCompile_InvalidOperation(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:        "http://127.0.0.1:9/",
		Tables:     []string{"public.orders"},
		Operations: []string{"upsert"},
	}, nil)
	require.Error(t, err)
}

func TestCompile_NonPositiveTimeout(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:     "http://127.0.0.1:9/",
		Tables:  []string{"public.orders"},
		Timeout: "0s",
	}, nil)
	require.Error(t, err)
}

func TestEnrich_NilEventAndDefaultWarn(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "x", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, nil)
	e.Enrich(context.Background(), nil) // no-op
	// Exercise slog.Warn path (warnFn unset).
	e.Enrich(context.Background(), testEvent("orders", event.OpInsert))
}

func TestSetWarnHelpers_NilSafe(t *testing.T) {
	t.Parallel()
	enrich.SetWarnForTest(nil, nil)
	enrich.SetWarnEveryForTest(nil, time.Second)
}

func TestWrap_NilEnricher(t *testing.T) {
	t.Parallel()
	inner := &memLog{}
	assert.Same(t, inner, enrich.Wrap(context.Background(), inner, nil))
}

func TestWrap_DisabledReturnsInner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	e := mustCompile(t, config.EnrichmentConfig{}, nil)
	wrapped := enrich.Wrap(context.Background(), inner, e)
	assert.Same(t, inner, wrapped)
}

func TestWrap_FailOpenStillAppends(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	m := observability.NewKaptantoMetrics()
	e := mustCompile(t, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, m)
	el := enrich.Wrap(context.Background(), inner, e)

	ev := testEvent("orders", event.OpUpdate)
	_, err = el.Append(ev)
	require.NoError(t, err)
	assert.Nil(t, ev.AIContext)
	assert.Equal(t, float64(1), failureCount(m, enrich.ReasonStatus))

	var found bool
	for p := uint32(0); p < 64; p++ {
		ents, err := el.ReadPartition(context.Background(), p, 1, 10)
		require.NoError(t, err)
		for _, ent := range ents {
			if ent.Event.IdempotencyKey == ev.IdempotencyKey {
				found = true
				assert.Nil(t, ent.Event.AIContext)
			}
		}
	}
	assert.True(t, found, "unenriched event must still be durable (AIC-01)")
}

func TestCompile_InvalidTimeout(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:     "http://127.0.0.1:9/",
		Tables:  []string{"public.orders"},
		Timeout: "not-a-duration",
	}, nil)
	require.Error(t, err)
}

func TestCompile_InvalidURL(t *testing.T) {
	t.Parallel()
	_, err := enrich.Compile(config.EnrichmentConfig{
		URL:    "://bad",
		Tables: []string{"public.orders"},
	}, nil)
	require.Error(t, err)
}

func TestConfig_EnrichmentYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `
source: postgres://localhost/db
enrichment:
  url: http://enricher:8080/enrich
  tables: ["public.orders", "public.*"]
  operations: [insert]
  timeout: 100ms
  auth-token: ${TOKEN}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "http://enricher:8080/enrich", cfg.Enrichment.URL)
	assert.Equal(t, []string{"public.orders", "public.*"}, cfg.Enrichment.Tables)
	assert.Equal(t, []string{"insert"}, cfg.Enrichment.Operations)
	assert.Equal(t, "100ms", cfg.Enrichment.Timeout)
	assert.Equal(t, "${TOKEN}", cfg.Enrichment.AuthToken)
}

// --- Benchmarks ---

func BenchmarkEnrich_Disabled(b *testing.B) {
	e := mustCompile(b, config.EnrichmentConfig{}, nil)
	ev := testEvent("orders", event.OpInsert)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Enrich(context.Background(), ev)
	}
}

func BenchmarkEnrich_EnabledNonMatching(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.Fatal("HTTP must not be called for non-matching events")
	}))
	b.Cleanup(srv.Close)

	e := mustCompile(b, config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, nil)
	ev := testEvent("users", event.OpInsert) // non-matching table
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Enrich(context.Background(), ev)
	}
}
