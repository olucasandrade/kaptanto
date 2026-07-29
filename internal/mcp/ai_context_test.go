package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDrain_AIContextRipple covers G3-19 #15 for MCP drains:
// present ai_context appears in drained events; absent is omitted.
func TestDrain_AIContextRipple(t *testing.T) {
	t.Setenv("MCP_AI_CTX", "ai-ctx-secret")
	metrics := observability.NewKaptantoMetrics()
	s, probe := newSubServer(t, []config.MCPAPIKey{
		{Name: "agent", Key: "${MCP_AI_CTX}", Tables: []string{"public.orders"}},
	}, 16, 4, metrics)

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	subRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	aiCtx := json.RawMessage(`{"intent":"fulfill","suggested_actions":["notify"]}`)
	require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{
		Seq: 1,
		Event: &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: event.OpInsert,
			Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1}`),
			AIContext: aiCtx,
		},
	}))
	require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{
		Seq: 2,
		Event: &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: event.OpUpdate,
			Key: json.RawMessage(`{"id":2}`), After: json.RawMessage(`{"id":2}`),
		},
	}))

	drainRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID, "max": 10},
	})
	require.NoError(t, err)
	require.False(t, drainRes.IsError, contentText(drainRes))

	var drainOut struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainRes)), &drainOut))
	require.Len(t, drainOut.Events, 2)

	require.Contains(t, drainOut.Events[0], "ai_context")
	gotAI, err := json.Marshal(drainOut.Events[0]["ai_context"])
	require.NoError(t, err)
	assert.JSONEq(t, string(aiCtx), string(gotAI))

	assert.NotContains(t, drainOut.Events[1], "ai_context")
}

func TestDrain_AIContextRedactedWhenColumnsMasked(t *testing.T) {
	t.Setenv("MCP_AI_REDACT", "ai-redact-secret")
	s, probe := newSubServer(t, []config.MCPAPIKey{
		{Name: "agent", Key: "${MCP_AI_REDACT}", Tables: []string{"public.orders"},
			Redact: []config.MCPRedactConfig{{
				Tables:  []string{"public.orders"},
				Columns: []string{"email"},
			}},
		},
	}, 16, 4, nil)

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	subRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	aiCtx := json.RawMessage(`{"email":"a@b.c","intent":"fulfill"}`)
	require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{
		Seq: 1,
		Event: &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: event.OpInsert,
			Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1,"email":"a@b.c"}`),
			AIContext: aiCtx,
		},
	}))

	drainRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID, "max": 10},
	})
	require.NoError(t, err)
	require.False(t, drainRes.IsError, contentText(drainRes))

	var drainOut struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainRes)), &drainOut))
	require.Len(t, drainOut.Events, 1)
	assert.NotContains(t, drainOut.Events[0], "ai_context")
	after := drainOut.Events[0]["after"].(map[string]any)
	assert.Equal(t, "***", after["email"])
}
