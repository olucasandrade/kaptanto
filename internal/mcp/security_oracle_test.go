package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolPath_SubscriptionOwnershipOracle verifies foreign subscription IDs
// return the same client-visible error as unknown IDs for unsubscribe and
// get_recent_events (no existence oracle across keys).
func TestToolPath_SubscriptionOwnershipOracle(t *testing.T) {
	t.Setenv("MCP_ORA_A", "ora-secret-a")
	t.Setenv("MCP_ORA_B", "ora-secret-b")
	s, _ := newSubServer(t, []config.MCPAPIKey{
		{Name: "alice", Key: "${MCP_ORA_A}", Tables: []string{"public.orders"}},
		{Name: "bob", Key: "${MCP_ORA_B}", Tables: []string{"public.orders"}},
	}, 8, 16, nil)

	ctxA := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	ctxB := mcp.ContextWithPrincipal(context.Background(), s.Keys()[1])
	csA := connectInProcess(t, ctxA, s)
	defer func() { _ = csA.Close() }()
	csB := connectInProcess(t, ctxB, s)
	defer func() { _ = csB.Close() }()

	subRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	unknownID := "mcp:alice:00000000000000000000000000"

	for _, tool := range []string{"unsubscribe", "get_recent_events"} {
		t.Run(tool, func(t *testing.T) {
			foreign, err := csB.CallTool(ctxB, &sdk.CallToolParams{
				Name: tool, Arguments: map[string]any{"subscription_id": subOut.SubscriptionID},
			})
			require.NoError(t, err)
			require.True(t, foreign.IsError)
			foreignMsg := contentText(foreign)

			unknown, err := csB.CallTool(ctxB, &sdk.CallToolParams{
				Name: tool, Arguments: map[string]any{"subscription_id": unknownID},
			})
			require.NoError(t, err)
			require.True(t, unknown.IsError)
			assert.Equal(t, foreignMsg, contentText(unknown))
			assert.Contains(t, foreignMsg, "subscription not found")
		})
	}
}
