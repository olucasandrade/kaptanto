package action_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// TestLambda_AIContextRipple covers G3-19 #15 for action payloads:
// present ai_context is forwarded in the Lambda body; absent is omitted.
func TestLambda_AIContextRipple(t *testing.T) {
	withLambdaAWSCreds(t)
	aiCtx := json.RawMessage(`{"intent":"escalate","custom":{"prio":1}}`)

	t.Run("present", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusAccepted)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("LAMBDA_AI_URL", srv.URL)

		reg := action.NewRegistry()
		reg.Register(action.LambdaType{})
		consumers, err := action.BuildConsumersWithRegistry(&config.Config{
			Actions: []config.ActionConfig{{
				Name: "ai-ctx",
				Type: "lambda",
				Params: map[string]string{
					"function-url": "${LAMBDA_AI_URL}",
					"region":       "us-east-1",
					"invocation":   "async",
				},
			}},
		}, observability.NewKaptantoMetrics(), reg)
		require.NoError(t, err)
		require.Len(t, consumers, 1)

		entry := eventlog.LogEntry{
			Seq: 1,
			Event: &event.ChangeEvent{
				Schema: "public", Table: "orders", Operation: event.OpInsert,
				Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1}`),
				AIContext: aiCtx,
			},
		}
		require.NoError(t, consumers[0].Deliver(context.Background(), entry))
		require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))

		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(gotBody, &m))
		require.Contains(t, m, "ai_context")
		assert.JSONEq(t, string(aiCtx), string(m["ai_context"]))
	})

	t.Run("absent_omitted", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusAccepted)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("LAMBDA_AI_URL2", srv.URL)

		reg := action.NewRegistry()
		reg.Register(action.LambdaType{})
		consumers, err := action.BuildConsumersWithRegistry(&config.Config{
			Actions: []config.ActionConfig{{
				Name: "no-ai",
				Type: "lambda",
				Params: map[string]string{
					"function-url": "${LAMBDA_AI_URL2}",
					"region":       "us-east-1",
					"invocation":   "async",
				},
			}},
		}, observability.NewKaptantoMetrics(), reg)
		require.NoError(t, err)

		entry := eventlog.LogEntry{
			Seq: 1,
			Event: &event.ChangeEvent{
				Schema: "public", Table: "orders", Operation: event.OpInsert,
				Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1}`),
			},
		}
		require.NoError(t, consumers[0].Deliver(context.Background(), entry))
		require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))

		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(gotBody, &m))
		assert.NotContains(t, m, "ai_context")
	})
}
