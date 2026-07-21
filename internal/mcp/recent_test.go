package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEventByID_HitMissACLRedaction(t *testing.T) {
	t.Setenv("MCP_RECENT_KEY", "recent-secret")
	idGen := event.NewIDGenerator()

	s, probe := newSubServer(t, []config.MCPAPIKey{{
		Name:   "agent",
		Key:    "${MCP_RECENT_KEY}",
		Tables: []string{"public.orders"},
		Redact: []config.MCPRedactConfig{{
			Tables:  []string{"public.orders"},
			Columns: []string{"email"},
		}},
	}}, 1024, 16, nil)
	require.True(t, s.RecentIndexActive())
	assert.Equal(t, mcp.DefaultRecentIndexSize, s.RecentIndexCapacity())
	assert.Equal(t, 1, probe.ConsumerCount()) // internal recent consumer only

	hitID := idGen.New()
	hit := &event.ChangeEvent{
		ID:        hitID,
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpInsert,
		Key:       json.RawMessage(`{"id":1}`),
		After:     json.RawMessage(`{"id":1,"email":"a@x.com","status":"open"}`),
	}
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: 1, Event: hit}))
	assert.Equal(t, 1, s.RecentIndexLen())

	deniedID := idGen.New()
	denied := &event.ChangeEvent{
		ID:        deniedID,
		Schema:    "public",
		Table:     "users",
		Operation: event.OpInsert,
		Key:       json.RawMessage(`{"id":2}`),
		After:     json.RawMessage(`{"id":2,"name":"bob"}`),
	}
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: 2, Event: denied}))

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	// Hit + redaction.
	hitRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_event_by_id",
		Arguments: map[string]any{"id": hitID.String()},
	})
	require.NoError(t, err)
	require.False(t, hitRes.IsError, contentText(hitRes))
	var hitOut struct {
		Event map[string]any `json:"event"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(hitRes)), &hitOut))
	after := hitOut.Event["after"].(map[string]any)
	assert.Equal(t, "***", after["email"])
	assert.Equal(t, "open", after["status"])

	// ACL denied (event in index, table not allowed).
	denyRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_event_by_id",
		Arguments: map[string]any{"id": deniedID.String()},
	})
	require.NoError(t, err)
	require.True(t, denyRes.IsError)
	assert.Contains(t, contentText(denyRes), "table not accessible")

	// Miss — never indexed.
	missID := idGen.New()
	missRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_event_by_id",
		Arguments: map[string]any{"id": missID.String()},
	})
	require.NoError(t, err)
	require.True(t, missRes.IsError)
	assert.Contains(t, contentText(missRes), "event not in recent index (last 10000 events)")
	assert.Contains(t, contentText(missRes), "use get_recent_events on a subscription")
}

func TestRecentIndex_BoundedEviction(t *testing.T) {
	t.Setenv("MCP_RECENT_BOUND", "bound-secret")
	const capN = 4
	idGen := event.NewIDGenerator()

	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	probe := newProbe(rtr)
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_RECENT_BOUND}"}},
		},
		DataDir:         t.TempDir(),
		Auditor:         mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		RecentIndexSize: capN,
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	defer func() { _ = s.Close() }()
	s.SetRouter(probe)
	assert.Equal(t, capN, s.RecentIndexCapacity())

	var ids []ulid.ULID
	for i := 0; i < capN+2; i++ {
		id := idGen.New()
		ids = append(ids, id)
		ev := &event.ChangeEvent{
			ID: id, Schema: "public", Table: "orders", Operation: event.OpInsert,
			Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1}`),
		}
		require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: uint64(i + 1), Event: ev}))
	}
	assert.Equal(t, capN, s.RecentIndexLen())

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	// Oldest two aged out.
	for _, aged := range ids[:2] {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{
			Name:      "get_event_by_id",
			Arguments: map[string]any{"id": aged.String()},
		})
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, contentText(res), "event not in recent index (last 4 events)")
	}
	// Newest still present.
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_event_by_id",
		Arguments: map[string]any{"id": ids[len(ids)-1].String()},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, contentText(res))
}

func TestRecentIndex_DisabledNoInternalConsumer(t *testing.T) {
	s, err := mcp.New(mcp.Options{
		Config:  config.MCPConfig{Enabled: false},
		DataDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Nil(t, s)

	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	assert.Equal(t, 0, rtr.ConsumerCount())
}

func TestMCP_WiringStartupShutdown(t *testing.T) {
	t.Setenv("MCP_WIRE_KEY", "wire-secret")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	probe := newProbe(rtr)

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_WIRE_KEY}"}},
		},
		DataDir:  t.TempDir(),
		Auditor:  mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Listener: ln,
	})
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.False(t, s.RecentIndexActive())
	assert.Equal(t, 0, probe.ConsumerCount())

	s.SetRouter(probe)
	require.True(t, s.RecentIndexActive())
	assert.Equal(t, 1, probe.ConsumerCount())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 5*time.Millisecond)

	// Create a subscription so shutdown must clear both recent + sub consumers.
	httpCtx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, httpCtx, s)
	subRes, err := cs.CallTool(httpCtx, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	assert.Equal(t, 2, probe.ConsumerCount())
	_ = cs.Close()

	cancel()
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	require.NoError(t, s.Close())
	assert.Equal(t, 0, probe.ConsumerCount(), "shutdown must unregister internal recent + all subs")
	assert.False(t, s.RecentIndexActive())
	assert.Equal(t, 0, s.ActiveSubscriptionCount())
}

func TestGetEventByID_EmptyIDAndUnauth(t *testing.T) {
	t.Setenv("MCP_RECENT_EDGE", "edge-secret")
	s, probe := newSubServer(t, []config.MCPAPIKey{{
		Name: "agent", Key: "${MCP_RECENT_EDGE}",
	}}, 1024, 16, nil)

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	empty, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name: "get_event_by_id", Arguments: map[string]any{"id": "  "},
	})
	require.NoError(t, err)
	require.True(t, empty.IsError)
	assert.Contains(t, contentText(empty), "id is required")

	// Nil event / zero ULID ignored by index.
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: 1, Event: nil}))
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{
		Seq: 2, Event: &event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert},
	}))
	assert.Equal(t, 0, s.RecentIndexLen())

	// Duplicate put refreshes in place.
	id := event.NewIDGenerator().New()
	ev := &event.ChangeEvent{
		ID: id, Schema: "public", Table: "orders", Operation: event.OpInsert,
		Key: json.RawMessage(`{"id":1}`), After: json.RawMessage(`{"id":1,"v":1}`),
	}
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: 3, Event: ev}))
	ev2 := *ev
	ev2.After = json.RawMessage(`{"id":1,"v":2}`)
	require.NoError(t, probe.Deliver("mcp:internal:recent", eventlog.LogEntry{Seq: 4, Event: &ev2}))
	assert.Equal(t, 1, s.RecentIndexLen())

	hit, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name: "get_event_by_id", Arguments: map[string]any{"id": id.String()},
	})
	require.NoError(t, err)
	require.False(t, hit.IsError, contentText(hit))
	assert.Contains(t, structuredOrText(hit), `"v":2`)
}

func TestSetRouter_NilClearsRecent(t *testing.T) {
	t.Setenv("MCP_RECENT_NIL", "nil-secret")
	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	probe := newProbe(rtr)
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_RECENT_NIL}"}},
		},
		DataDir:         t.TempDir(),
		Auditor:         mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		RecentIndexSize: 0, // default capacity
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	s.SetRouter(probe)
	assert.True(t, s.RecentIndexActive())
	assert.Equal(t, mcp.DefaultRecentIndexSize, s.RecentIndexCapacity())
	assert.Equal(t, 1, s.ConsumerCount())

	// Re-bind to same registry (re-register path).
	s.SetRouter(probe)
	assert.Equal(t, 1, probe.ConsumerCount())

	s.SetRouter(nil)
	assert.False(t, s.RecentIndexActive())
	assert.Equal(t, 0, s.RecentIndexLen())
	assert.Equal(t, 0, s.ConsumerCount())
}

func TestRecentIndex_DefaultCapacityClamp(t *testing.T) {
	t.Setenv("MCP_RECENT_CLAMP", "clamp-secret")
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_RECENT_CLAMP}"}},
		},
		DataDir:         t.TempDir(),
		Auditor:         mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		RecentIndexSize: -5,
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	s.SetRouter(rtr)
	assert.Equal(t, mcp.DefaultRecentIndexSize, s.RecentIndexCapacity())
}

func TestSubscriptionResource_ReadHint(t *testing.T) {
	t.Setenv("MCP_RES_READ", "res-secret")
	s, _ := newSubServer(t, []config.MCPAPIKey{{
		Name: "agent", Key: "${MCP_RES_READ}",
	}}, 8, 16, nil)

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	subRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name: "subscribe_to_changes", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
		ResourceURI    string `json:"resource_uri"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))

	contents, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: subOut.ResourceURI})
	require.NoError(t, err)
	require.NotEmpty(t, contents.Contents)
	text := contents.Contents[0].Text
	assert.Contains(t, text, subOut.SubscriptionID)
	assert.Contains(t, text, "get_recent_events")
}
