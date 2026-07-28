package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCP_Integration_SubscribeDrainACL wires MCP to a real router with in-process
// delivery and asserts ACL redaction on drained subscription events (MCP-01).
func TestMCP_Integration_SubscribeDrainACL(t *testing.T) {
	t.Setenv("MCP_INT_A", "integration-secret-a")
	t.Setenv("MCP_INT_B", "integration-secret-b")

	rtr := router.NewRouter(&memEventLog{}, 4, nil)
	probe := newProbe(rtr)

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled:          true,
			RingSize:         64,
			MaxSubscriptions: 8,
			APIKeys: []config.MCPAPIKey{
				{
					Name:   "alice",
					Key:    "${MCP_INT_A}",
					Tables: []string{"public.orders"},
					Redact: []config.MCPRedactConfig{{
						Tables:  []string{"public.orders"},
						Columns: []string{"email"},
					}},
				},
				{
					Name:   "bob",
					Key:    "${MCP_INT_B}",
					Tables: []string{"public.orders"},
					Redact: []config.MCPRedactConfig{{
						Tables:  []string{"public.orders"},
						Columns: []string{"phone"},
					}},
				},
			},
		},
		DataDir: t.TempDir(),
		Auditor: mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: observability.NewKaptantoMetrics(),
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	s.SetRouter(probe)
	t.Cleanup(func() { _ = s.Close() })

	ctxA := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	csA := connectInProcess(t, ctxA, s)
	t.Cleanup(func() { _ = csA.Close() })

	subRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name: "subscribe_to_changes",
		Arguments: map[string]any{
			"tables":     []string{"public.orders"},
			"operations": []string{"insert"},
		},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	entry := eventlog.LogEntry{Seq: 1, Event: &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpInsert,
		After:     json.RawMessage(`{"id":1,"email":"a@x.com","phone":"555"}`),
		Key:       json.RawMessage(`{"id":1}`),
	}}
	require.NoError(t, probe.Deliver(subOut.SubscriptionID, entry))

	drainRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID},
	})
	require.NoError(t, err)
	require.False(t, drainRes.IsError, contentText(drainRes))
	var drainOut struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainRes)), &drainOut))
	require.Len(t, drainOut.Events, 1)
	after := drainOut.Events[0]["after"].(map[string]any)
	assert.Equal(t, "***", after["email"])
	assert.Equal(t, "555", after["phone"])
}
