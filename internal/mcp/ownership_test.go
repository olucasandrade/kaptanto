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

// TestSubscriptionResource_OwnershipEnforced verifies MCP-01 on the resource
// surface: a key can read and subscribe to its own subscription resource, but
// a foreign key gets the same "not found / unknown resource" surface as an
// unknown id (no existence oracle, no cross-key metadata leak).
func TestSubscriptionResource_OwnershipEnforced(t *testing.T) {
	t.Setenv("MCP_OWN_A", "own-secret-a")
	t.Setenv("MCP_OWN_B", "own-secret-b")
	s, _ := newSubServer(t, []config.MCPAPIKey{
		{Name: "alice", Key: "${MCP_OWN_A}", Tables: []string{"public.orders"}},
		{Name: "bob", Key: "${MCP_OWN_B}", Tables: []string{"public.orders"}},
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
		ResourceURI    string `json:"resource_uri"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	// The owning key can read and subscribe to its subscription resource.
	contents, err := csA.ReadResource(ctxA, &sdk.ReadResourceParams{URI: subOut.ResourceURI})
	require.NoError(t, err)
	require.NotEmpty(t, contents.Contents)
	require.NoError(t, csA.Subscribe(ctxA, &sdk.SubscribeParams{URI: subOut.ResourceURI}))
	require.NoError(t, csA.Unsubscribe(ctxA, &sdk.UnsubscribeParams{URI: subOut.ResourceURI}))

	// A foreign key cannot read, subscribe, or unsubscribe — and cannot
	// distinguish a foreign id from an unknown one.
	_, err = csB.ReadResource(ctxB, &sdk.ReadResourceParams{URI: subOut.ResourceURI})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription not found")
	require.Error(t, csB.Subscribe(ctxB, &sdk.SubscribeParams{URI: subOut.ResourceURI}))
	require.Error(t, csB.Unsubscribe(ctxB, &sdk.UnsubscribeParams{URI: subOut.ResourceURI}))
}
