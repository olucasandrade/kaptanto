package vector_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeEmbedder struct {
	mu        sync.Mutex
	cap       int
	calls     int
	texts     [][]string
	fn        func(texts []string) ([][]float32, error)
	dim       int
	callCount *int // optional shared counter
}

func (e *fakeEmbedder) Cap() int      { return e.cap }
func (e *fakeEmbedder) Model() string { return "fake" }

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.callCount != nil {
		*e.callCount++
	}
	cp := append([]string(nil), texts...)
	e.texts = append(e.texts, cp)
	if e.fn != nil {
		return e.fn(texts)
	}
	dim := e.dim
	if dim <= 0 {
		dim = 3
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, dim)
		out[i][0] = float32(len(texts[i]))
	}
	return out, nil
}

type storeOp struct {
	kind string // "upsert" | "delete"
	ids  []string
}

type fakeStore struct {
	mu      sync.Mutex
	ops     []storeOp
	upserts [][]vector.Record
	upsertFn func(recs []vector.Record) error
	deleteFn func(ids []string) error
	pingErr  error
}

func (s *fakeStore) Upsert(_ context.Context, recs []vector.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	s.ops = append(s.ops, storeOp{kind: "upsert", ids: ids})
	cp := make([]vector.Record, len(recs))
	copy(cp, recs)
	s.upserts = append(s.upserts, cp)
	if s.upsertFn != nil {
		return s.upsertFn(recs)
	}
	return nil
}

func (s *fakeStore) Delete(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]string(nil), ids...)
	s.ops = append(s.ops, storeOp{kind: "delete", ids: cp})
	if s.deleteFn != nil {
		return s.deleteFn(ids)
	}
	return nil
}

func (s *fakeStore) Ping(context.Context) error { return s.pingErr }
func (s *fakeStore) Close() error                { return nil }

type spyCache struct {
	mu      sync.Mutex
	puts    map[string][]byte
	dels    []string
	skipPut bool
	putErr  error
}

func newSpyCache() *spyCache {
	return &spyCache{puts: make(map[string][]byte)}
}

func (c *spyCache) Unchanged(id string, hash []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	stored, ok := c.puts[id]
	if !ok || len(stored) != len(hash) {
		return false
	}
	for i := range stored {
		if stored[i] != hash[i] {
			return false
		}
	}
	return true
}

func (c *spyCache) Put(id string, hash []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.skipPut {
		return nil // crash-sim: pretend Put never happened
	}
	if c.putErr != nil {
		return c.putErr
	}
	cp := make([]byte, len(hash))
	copy(cp, hash)
	c.puts[id] = cp
	return nil
}

func (c *spyCache) Del(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.puts, id)
	c.dels = append(c.dels, id)
	return nil
}

func (c *spyCache) Close() error { return nil }

func testExtractor(t *testing.T) *vector.Extractor {
	t.Helper()
	e, err := vector.NewExtractor(config.VectorSourceConfig{Columns: []string{"title", "body"}})
	require.NoError(t, err)
	return e
}

func makeEntry(seq uint64, op event.Operation, table string, key map[string]any, after map[string]any) eventlog.LogEntry {
	keyRaw, _ := json.Marshal(key)
	var afterRaw json.RawMessage
	if after != nil {
		afterRaw, _ = json.Marshal(after)
	}
	return eventlog.LogEntry{
		Seq:         seq,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Operation: op,
			Schema:    "public",
			Table:     table,
			Key:       keyRaw,
			After:     afterRaw,
			Timestamp: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		},
	}
}

func newTestConsumer(t *testing.T, emb *fakeEmbedder, store *fakeStore, cache vector.TextHashCacheForTest) *vector.VectorSinkConsumer {
	t.Helper()
	if emb == nil {
		emb = &fakeEmbedder{cap: 96}
	}
	if store == nil {
		store = &fakeStore{}
	}
	if cache == nil {
		cache = newSpyCache()
	}
	c := vector.NewVectorSinkConsumerWithDeps("vector", testExtractor(t), emb, store, cache, nil, 96)
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)
	t.Cleanup(c.Close)
	return c
}

func TestDeliver_ControlAck(t *testing.T) {
	c := newTestConsumer(t, nil, nil, nil)
	err := c.Deliver(context.Background(), makeEntry(1, event.OpControl, "t", map[string]any{"id": 1}, nil))
	require.NoError(t, err)
	require.NoError(t, c.FlushBatch(context.Background(), 0))
}

func TestDeliver_EmptyTextSkipped(t *testing.T) {
	store := &fakeStore{}
	ext, err := vector.NewExtractor(config.VectorSourceConfig{Template: `{{/* empty */}}`})
	require.NoError(t, err)
	c := vector.NewVectorSinkConsumerWithDeps("vector", ext, &fakeEmbedder{cap: 96}, store, newSpyCache(), nil, 96)
	c.SetMetrics(observability.NewKaptantoMetrics())
	t.Cleanup(c.Close)

	err = c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{
		"title": "x", "body": "y",
	}))
	require.NoError(t, err)
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Empty(t, store.ops)
	assert.Equal(t, float64(1), testutil.ToFloat64(c.MetricsForTest().VectorSkippedTotal.WithLabelValues(vector.SkipReasonEmpty)))
}

func TestDeliver_UnchangedHashSkip_VEC01(t *testing.T) {
	emb := &fakeEmbedder{cap: 96}
	store := &fakeStore{}
	cache := newSpyCache()
	c := newTestConsumer(t, emb, store, cache)

	entry := makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{
		"title": "hello", "body": "world",
	})
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, 1, emb.calls)
	assert.Len(t, store.ops, 1)

	// Same text again → hash skip, no embed/upsert.
	entry2 := makeEntry(2, event.OpUpdate, "docs", map[string]any{"id": 1}, map[string]any{
		"title": "hello", "body": "world", "other": "ignored",
	})
	require.NoError(t, c.Deliver(context.Background(), entry2))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, 1, emb.calls, "VEC-01: unchanged text must not re-embed")
	assert.Len(t, store.ops, 1)
	assert.Equal(t, float64(1), testutil.ToFloat64(c.MetricsForTest().VectorSkippedTotal.WithLabelValues(vector.SkipReasonUnchanged)))
}

func TestFlushBatch_InsertThenDeleteOrder_VEC02(t *testing.T) {
	store := &fakeStore{}
	c := newTestConsumer(t, nil, store, nil)

	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a", "body": "b"})))
	require.NoError(t, c.Deliver(context.Background(), makeEntry(2, event.OpDelete, "docs", map[string]any{"id": 1}, nil)))
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	require.Len(t, store.ops, 2)
	assert.Equal(t, "upsert", store.ops[0].kind)
	assert.Equal(t, "delete", store.ops[1].kind)
	assert.Equal(t, store.ops[0].ids[0], store.ops[1].ids[0])
}

func TestFlushBatch_InterleavingProperty_VEC02(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 20; trial++ {
		store := &fakeStore{}
		c := newTestConsumer(t, nil, store, nil)

		type step struct {
			op  event.Operation
			key int
			seq uint64
		}
		var steps []step
		seq := uint64(1)
		for _, key := range []int{1, 2, 3} {
			n := 2 + rng.Intn(3)
			for i := 0; i < n; i++ {
				op := event.OpInsert
				if rng.Float64() < 0.4 {
					op = event.OpDelete
				}
				steps = append(steps, step{op: op, key: key, seq: seq})
				seq++
			}
		}
		rng.Shuffle(len(steps), func(i, j int) { steps[i], steps[j] = steps[j], steps[i] })

		for _, s := range steps {
			key := map[string]any{"id": fmt.Sprintf("%d", s.key)}
			var err error
			if s.op == event.OpDelete {
				err = c.Deliver(context.Background(), makeEntry(s.seq, event.OpDelete, "docs", key, nil))
			} else {
				err = c.Deliver(context.Background(), makeEntry(s.seq, event.OpInsert, "docs", key, map[string]any{
					"title": fmt.Sprintf("t-%d-%d", s.key, s.seq), "body": "x",
				}))
			}
			require.NoError(t, err)
		}
		require.NoError(t, c.FlushBatch(context.Background(), 0))

		// Per-key: reconstruct op order from store ops and compare to deliver order.
		perKeyWant := map[int][]string{}
		for _, s := range steps {
			kind := "upsert"
			if s.op == event.OpDelete {
				kind = "delete"
			}
			perKeyWant[s.key] = append(perKeyWant[s.key], kind)
		}
		perKeyGot := map[int][]string{}
		for _, op := range store.ops {
			for _, id := range op.ids {
				_, rest, ok := cutCanonical(id)
				require.True(t, ok)
				var keyObj map[string]any
				require.NoError(t, json.Unmarshal([]byte(rest), &keyObj))
				k := keyAsInt(t, keyObj["id"])
				perKeyGot[k] = append(perKeyGot[k], op.kind)
			}
		}
		assert.Equal(t, perKeyWant, perKeyGot, "trial %d: per-key order must match deliver order", trial)
	}
}

func keyAsInt(t *testing.T, v any) int {
	t.Helper()
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, err := x.Int64()
		require.NoError(t, err)
		return int(n)
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		require.NoError(t, err)
		return n
	default:
		t.Fatalf("unexpected key type %T", v)
		return 0
	}
}

func cutCanonical(id string) (table, keyJSON string, ok bool) {
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			return id[:i], id[i+1:], true
		}
	}
	return "", "", false
}

func TestFlushBatch_429NeverDLQ(t *testing.T) {
	emb := &fakeEmbedder{cap: 96, fn: func([]string) ([][]float32, error) {
		return nil, &vector.StatusError{Status: 429, Msg: "rate limited"}
	}}
	c := newTestConsumer(t, emb, nil, nil)
	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a", "body": "b"})))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	assert.False(t, errors.As(err, &pfe), "429 must never be PermanentFlushError")
	assert.True(t, vector.IsTransient(err))
}

func TestFlushBatch_400Isolation_EmbedCallCounts(t *testing.T) {
	emb := &fakeEmbedder{cap: 96}
	store := &fakeStore{}
	store.upsertFn = func(recs []vector.Record) error {
		if len(recs) > 1 {
			return &vector.StatusError{Status: 400, Msg: "batch rejected"}
		}
		if len(recs) == 1 && len(recs[0].Text) > 40 {
			return &vector.StatusError{Status: 400, Msg: "token limit"}
		}
		return nil
	}

	cache := newSpyCache()
	c := vector.NewVectorSinkConsumerWithDeps("vector", testExtractor(t), emb, store, cache, nil, 96)
	c.SetMetrics(observability.NewKaptantoMetrics())
	t.Cleanup(c.Close)

	huge := "HUGE_" + strings.Repeat("x", 50)

	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "ok1", "body": "x"})))
	require.NoError(t, c.Deliver(context.Background(), makeEntry(2, event.OpInsert, "docs", map[string]any{"id": 2}, map[string]any{"title": huge, "body": "y"})))
	require.NoError(t, c.Deliver(context.Background(), makeEntry(3, event.OpInsert, "docs", map[string]any{"id": 3}, map[string]any{"title": "ok3", "body": "z"})))

	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	require.True(t, errors.As(err, &pfe))
	assert.Equal(t, uint64(2), pfe.Seq)

	// Batch embed once (3 texts); isolation reuses cached vectors — no re-embed.
	assert.Equal(t, 1, emb.calls, "successes must not be re-embedded during isolation")
}

func TestFlushBatch_CrashSim_HashCachePostAck(t *testing.T) {
	emb := &fakeEmbedder{cap: 96}
	store := &fakeStore{}
	cache := newSpyCache()
	cache.skipPut = true // crash between upsert and Put
	c := newTestConsumer(t, emb, store, cache)

	entry := makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "hello", "body": "world"})
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, 1, emb.calls)
	assert.Len(t, store.ops, 1)
	assert.Empty(t, cache.puts, "crash-sim: Put must not have landed")

	// Recover: enable Put; re-deliver same text → re-embed (cache miss) then Put.
	cache.skipPut = false
	require.NoError(t, c.Deliver(context.Background(), makeEntry(2, event.OpUpdate, "docs", map[string]any{"id": 1}, map[string]any{"title": "hello", "body": "world"})))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, 2, emb.calls, "re-embed after crash before Put")
	assert.NotEmpty(t, cache.puts)

	// Converged: third delivery skips.
	require.NoError(t, c.Deliver(context.Background(), makeEntry(3, event.OpUpdate, "docs", map[string]any{"id": 1}, map[string]any{"title": "hello", "body": "world"})))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, 2, emb.calls)
}

func TestFlushBatch_EmbedderCountMismatchTransient(t *testing.T) {
	emb := &fakeEmbedder{cap: 96, fn: func(texts []string) ([][]float32, error) {
		return [][]float32{{0.1}}, nil // fewer than texts
	}}
	c := newTestConsumer(t, emb, nil, nil)
	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a", "body": "b"})))
	require.NoError(t, c.Deliver(context.Background(), makeEntry(2, event.OpInsert, "docs", map[string]any{"id": 2}, map[string]any{"title": "c", "body": "d"})))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VEC-02")
	var pfe *router.PermanentFlushError
	assert.False(t, errors.As(err, &pfe))
	assert.True(t, vector.IsTransient(err))
}

func TestDeliver_TemplateRuntimeErrorPermanent(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{Template: "{{.MissingField.Boom}}"})
	require.NoError(t, err)
	c := vector.NewVectorSinkConsumerWithDeps("vector", ext, &fakeEmbedder{cap: 96}, &fakeStore{}, newSpyCache(), nil, 96)
	t.Cleanup(c.Close)
	err = c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a"}))
	require.Error(t, err)
	var perm interface{ PermanentDelivery() }
	require.True(t, errors.As(err, &perm))
}

func TestDLQReplay_AgainstFakeStore(t *testing.T) {
	// Simulate: poison once, then "fix" store and replay the same event via Deliver.
	store := &fakeStore{}
	fail := true
	store.upsertFn = func([]vector.Record) error {
		if fail {
			return &vector.StatusError{Status: 400, Msg: "bad"}
		}
		return nil
	}
	c := newTestConsumer(t, nil, store, nil)
	entry := makeEntry(9, event.OpInsert, "docs", map[string]any{"id": 9}, map[string]any{"title": "replay", "body": "me"})
	require.NoError(t, c.Deliver(context.Background(), entry))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	require.True(t, errors.As(err, &pfe))
	assert.Equal(t, uint64(9), pfe.Seq)

	fail = false
	require.NoError(t, c.Deliver(context.Background(), entry)) // dlq replay re-extracts/re-embeds
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	require.GreaterOrEqual(t, len(store.ops), 1)
	assert.Equal(t, "upsert", store.ops[len(store.ops)-1].kind)
}

func TestSplitRuns(t *testing.T) {
	runs := vector.SplitRunsForTest(nil)
	assert.Empty(t, runs)
}

func TestStatusClassification(t *testing.T) {
	assert.True(t, vector.IsTransient(&vector.StatusError{Status: 429}))
	assert.True(t, vector.IsTransient(&vector.StatusError{Status: 503}))
	assert.True(t, vector.IsTransient(&vector.StatusError{Status: 408}))
	assert.True(t, vector.IsTransient(errors.New("network")))
	assert.False(t, vector.IsPoison(&vector.StatusError{Status: 429}))
	assert.True(t, vector.IsPoison(&vector.StatusError{Status: 400}))
	assert.False(t, vector.IsTransient(&vector.StatusError{Status: 400}))
}

func TestPingAndClose(t *testing.T) {
	store := &fakeStore{pingErr: errors.New("down")}
	c := newTestConsumer(t, nil, store, nil)
	assert.Error(t, c.Ping())
	c.Close()
	c.Close() // idempotent
}

func TestStore429Transient(t *testing.T) {
	emb := &fakeEmbedder{cap: 96}
	store := &fakeStore{upsertFn: func([]vector.Record) error {
		return &vector.StatusError{Status: 429, Msg: "store rate limit"}
	}}
	c := newTestConsumer(t, emb, store, nil)
	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a", "body": "b"})))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	assert.False(t, errors.As(err, &pfe))
}

func TestDeleteThenUpsertRuns(t *testing.T) {
	store := &fakeStore{}
	c := newTestConsumer(t, nil, store, nil)
	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpDelete, "docs", map[string]any{"id": 1}, nil)))
	require.NoError(t, c.Deliver(context.Background(), makeEntry(2, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{"title": "a", "body": "b"})))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	require.Len(t, store.ops, 2)
	assert.Equal(t, "delete", store.ops[0].kind)
	assert.Equal(t, "upsert", store.ops[1].kind)
}

func TestMetadataColumns(t *testing.T) {
	store := &fakeStore{}
	ext := testExtractor(t)
	c := vector.NewVectorSinkConsumerWithDeps("vector", ext, &fakeEmbedder{cap: 96}, store, newSpyCache(), []string{"author"}, 96)
	t.Cleanup(c.Close)
	require.NoError(t, c.Deliver(context.Background(), makeEntry(1, event.OpInsert, "docs", map[string]any{"id": 1}, map[string]any{
		"title": "a", "body": "b", "author": "ada",
	})))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	require.Len(t, store.upserts, 1)
	assert.Equal(t, "ada", store.upserts[0][0].Metadata["author"])
	assert.Equal(t, "docs", store.upserts[0][0].Metadata["table"])
}

func TestEmptyFlushBatch(t *testing.T) {
	c := newTestConsumer(t, nil, nil, nil)
	require.NoError(t, c.FlushBatch(context.Background(), 0))
}

func TestNewVectorSinkConsumer_MissingDims(t *testing.T) {
	cfg := config.VectorSinkConfig{
		Source:   config.VectorSourceConfig{Columns: []string{"title"}},
		Embedder: config.VectorEmbedderConfig{Provider: "openai", Model: "m", APIKey: "${K}"},
		Store:    config.VectorStoreConfig{Provider: "pgvector", DSN: "${D}"},
	}
	t.Setenv("K", "key")
	t.Setenv("D", "postgres://x")
	_, err := vector.NewVectorSinkConsumer("vector", cfg, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")
}

func TestNewVectorSinkConsumer_MissingEnv(t *testing.T) {
	cfg := config.VectorSinkConfig{
		Source:   config.VectorSourceConfig{Columns: []string{"title"}},
		Embedder: config.VectorEmbedderConfig{Provider: "openai", Model: "m", APIKey: "${MISSING_VEC_KEY}", Dimensions: 3},
		Store:    config.VectorStoreConfig{Provider: "pinecone", APIKey: "${MISSING_PC}", IndexHost: "idx.pinecone.io"},
	}
	_, err := vector.NewVectorSinkConsumer("vector", cfg, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing environment variable")
}

func TestStatusErrorMethods(t *testing.T) {
	var nilSE *vector.StatusError
	assert.Contains(t, nilSE.Error(), "nil")
	assert.Equal(t, "vector: HTTP 400", (&vector.StatusError{Status: 400}).Error())
	assert.False(t, vector.IsTransient(nil))
	assert.False(t, vector.IsPoison(nil))
}
