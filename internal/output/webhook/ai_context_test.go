package webhooksink_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	webhooksink "github.com/olucasandrade/kaptanto/internal/output/webhook"
)

// TestFlushBatch_AIContextRipple covers G3-19 #15 for webhook payloads:
// present ai_context flows through unchanged; absent is omitted.
func TestFlushBatch_AIContextRipple(t *testing.T) {
	aiCtx := json.RawMessage(`{"intent":"ship","entities":[]}`)

	t.Run("present", func(t *testing.T) {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		c, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{URL: srv.URL})
		require.NoError(t, err)
		t.Cleanup(c.Close)

		entry := eventlog.LogEntry{
			Seq: 1,
			Event: &event.ChangeEvent{
				Schema:    "public",
				Table:     "orders",
				Operation: event.OpInsert,
				Key:       json.RawMessage(`{"id":1}`),
				After:     json.RawMessage(`{"id":1}`),
				AIContext: aiCtx,
			},
		}
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))

		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &m))
		require.Contains(t, m, "ai_context")
		assert.JSONEq(t, string(aiCtx), string(m["ai_context"]))
	})

	t.Run("absent_omitted", func(t *testing.T) {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		c, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{URL: srv.URL})
		require.NoError(t, err)
		t.Cleanup(c.Close)

		entry := eventlog.LogEntry{
			Seq: 1,
			Event: &event.ChangeEvent{
				Schema:    "public",
				Table:     "orders",
				Operation: event.OpInsert,
				Key:       json.RawMessage(`{"id":1}`),
				After:     json.RawMessage(`{"id":1}`),
			},
		}
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))

		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &m))
		assert.NotContains(t, m, "ai_context")
	})
}
