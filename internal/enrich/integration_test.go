package enrich_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/enrich"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnrich_Integration_WrapSuccessDurable exercises AIC-01/02 against a real
// Badger EventLog with a local httptest enricher — no live Postgres required.
func TestEnrich_Integration_WrapSuccessDurable(t *testing.T) {
	const wantAI = `{"intent":"fulfill","score":1}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wantAI))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	m := observability.NewKaptantoMetrics()
	e, err := enrich.Compile(config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, m)
	require.NoError(t, err)
	el := enrich.Wrap(context.Background(), inner, e)

	ev := testEvent("orders", event.OpInsert)
	seq, err := el.Append(ev)
	require.NoError(t, err)
	require.NotZero(t, seq)
	require.NotNil(t, ev.AIContext)
	assert.JSONEq(t, wantAI, string(ev.AIContext))

	var found bool
	for p := uint32(0); p < 64; p++ {
		ents, err := el.ReadPartition(context.Background(), p, 1, 10)
		require.NoError(t, err)
		for _, ent := range ents {
			if ent.Event.IdempotencyKey != ev.IdempotencyKey {
				continue
			}
			found = true
			require.NotNil(t, ent.Event.AIContext)
			assert.JSONEq(t, wantAI, string(ent.Event.AIContext))
		}
	}
	assert.True(t, found, "enriched event must be durable in Badger")
}

// TestEnrich_Integration_FailOpenStillCheckpoints verifies fail-open enrichment
// still persists events when the sidecar returns 502.
func TestEnrich_Integration_FailOpenStillCheckpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inner, err := eventlog.Open(filepath.Join(dir, "events"), 64, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	m := observability.NewKaptantoMetrics()
	e, err := enrich.Compile(config.EnrichmentConfig{
		URL:     srv.URL,
		Tables:  []string{"public.orders"},
		Timeout: enrichTestTimeout,
	}, m)
	require.NoError(t, err)
	el := enrich.Wrap(context.Background(), inner, e)

	ev := testEvent("orders", event.OpUpdate)
	_, err = el.Append(ev)
	require.NoError(t, err)
	assert.Nil(t, ev.AIContext)

	var raw json.RawMessage
	for p := uint32(0); p < 64; p++ {
		ents, err := el.ReadPartition(context.Background(), p, 1, 10)
		require.NoError(t, err)
		for _, ent := range ents {
			if ent.Event.IdempotencyKey == ev.IdempotencyKey {
				raw = ent.Event.AIContext
				break
			}
		}
	}
	assert.Nil(t, raw, "fail-open must not attach ai_context")
}
