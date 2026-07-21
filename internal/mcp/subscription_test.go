package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	f        func()
	canceled bool
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) (cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{deadline: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		t.canceled = true
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	remaining := c.timers[:0]
	for _, t := range c.timers {
		if t.canceled {
			continue
		}
		if !t.deadline.After(now) {
			due = append(due, t)
			continue
		}
		remaining = append(remaining, t)
	}
	c.timers = remaining
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

type memEventLog struct{}

func (m *memEventLog) Append(*event.ChangeEvent) (uint64, error) { return 1, nil }
func (m *memEventLog) AppendBatch([]*event.ChangeEvent) ([]uint64, error) {
	return nil, nil
}
func (m *memEventLog) ReadPartition(context.Context, uint32, uint64, int) ([]eventlog.LogEntry, error) {
	return nil, nil
}
func (m *memEventLog) Close() error { return nil }

type probeRegistry struct {
	inner *router.Router
	mu    sync.Mutex
	byID  map[string]router.Consumer
}

func newProbe(rtr *router.Router) *probeRegistry {
	return &probeRegistry{inner: rtr, byID: make(map[string]router.Consumer)}
}

func (p *probeRegistry) Register(c router.Consumer) {
	p.mu.Lock()
	p.byID[c.ID()] = c
	p.mu.Unlock()
	p.inner.Register(c)
}

func (p *probeRegistry) Unregister(id string) bool {
	p.mu.Lock()
	delete(p.byID, id)
	p.mu.Unlock()
	return p.inner.Unregister(id)
}

func (p *probeRegistry) ConsumerCount() int { return p.inner.ConsumerCount() }

func (p *probeRegistry) Deliver(id string, entry eventlog.LogEntry) error {
	p.mu.Lock()
	c := p.byID[id]
	p.mu.Unlock()
	if c == nil {
		return fmt.Errorf("consumer %q not found", id)
	}
	return c.Deliver(context.Background(), entry)
}

func newSubServer(t *testing.T, keys []config.MCPAPIKey, ringSize, maxSubs int, metrics *observability.KaptantoMetrics) (*mcp.Server, *probeRegistry) {
	t.Helper()
	rtr := router.NewRouter(&memEventLog{}, 4, nil)
	probe := newProbe(rtr)
	if ringSize <= 0 {
		ringSize = 1024
	}
	if maxSubs <= 0 {
		maxSubs = 16
	}
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled:          true,
			MaxSubscriptions: maxSubs,
			RingSize:         ringSize,
			APIKeys:          keys,
		},
		DataDir:          t.TempDir(),
		Auditor:          mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:          metrics,
		SourceType:       mcp.SourcePostgres,
		ConfiguredTables: []string{"public.orders", "public.users"},
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	s.SetRouter(probe)
	t.Cleanup(func() { _ = s.Close() })
	return s, probe
}

func TestSubscriptionTools_MatrixViaInProcessClient(t *testing.T) {
	t.Setenv("MCP_SUB_A", "secret-a")
	t.Setenv("MCP_SUB_B", "secret-b")
	metrics := observability.NewKaptantoMetrics()

	s, probe := newSubServer(t, []config.MCPAPIKey{
		{
			Name:   "alice",
			Key:    "${MCP_SUB_A}",
			Tables: []string{"public.orders"},
			Redact: []config.MCPRedactConfig{{
				Tables:  []string{"public.orders"},
				Columns: []string{"email"},
			}},
		},
		{
			Name:   "bob",
			Key:    "${MCP_SUB_B}",
			Tables: []string{"public.orders"},
			Redact: []config.MCPRedactConfig{{
				Tables:  []string{"public.orders"},
				Columns: []string{"phone"},
			}},
		},
	}, 16, 2, metrics)

	baseline := probe.ConsumerCount()
	alice := s.Keys()[0]
	bob := s.Keys()[1]

	ctxA := mcp.ContextWithPrincipal(context.Background(), alice)
	csA := connectInProcess(t, ctxA, s)
	defer func() { _ = csA.Close() }()

	bad, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}, "where": "((bad"},
	})
	require.NoError(t, err)
	require.True(t, bad.IsError, contentText(bad))
	assert.Equal(t, baseline, probe.ConsumerCount())
	assert.Equal(t, 0, s.ActiveSubscriptionCount())

	subRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name: "subscribe_to_changes",
		Arguments: map[string]any{
			"tables":     []string{"public.orders"},
			"operations": []string{"insert", "update"},
		},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	var subOut struct {
		SubscriptionID string `json:"subscription_id"`
		ResourceURI    string `json:"resource_uri"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subRes)), &subOut))
	assert.True(t, strings.HasPrefix(subOut.SubscriptionID, "mcp:alice:"))
	assert.Equal(t, "kaptanto://subscriptions/"+subOut.SubscriptionID, subOut.ResourceURI)
	assert.Equal(t, baseline+1, probe.ConsumerCount())
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.MCPSubscriptionsActive))

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
		Events    []map[string]any `json:"events"`
		Dropped   int              `json:"dropped"`
		Remaining int              `json:"remaining"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainRes)), &drainOut))
	require.Len(t, drainOut.Events, 1)
	after := drainOut.Events[0]["after"].(map[string]any)
	assert.Equal(t, "***", after["email"])
	assert.Equal(t, "555", after["phone"])

	ctxB := mcp.ContextWithPrincipal(context.Background(), bob)
	csB := connectInProcess(t, ctxB, s)
	defer func() { _ = csB.Close() }()

	subBRes, err := csB.CallTool(ctxB, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}},
	})
	require.NoError(t, err)
	require.False(t, subBRes.IsError)
	var subBOut struct {
		SubscriptionID string `json:"subscription_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(subBRes)), &subBOut))

	entry2 := eventlog.LogEntry{Seq: 2, Event: &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpUpdate,
		After:     json.RawMessage(`{"id":2,"email":"b@x.com","phone":"999"}`),
		Key:       json.RawMessage(`{"id":2}`),
	}}
	require.NoError(t, probe.Deliver(subBOut.SubscriptionID, entry2))

	drainB, err := csB.CallTool(ctxB, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subBOut.SubscriptionID},
	})
	require.NoError(t, err)
	require.False(t, drainB.IsError)
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainB)), &drainOut))
	require.Len(t, drainOut.Events, 1)
	afterB := drainOut.Events[0]["after"].(map[string]any)
	assert.Equal(t, "b@x.com", afterB["email"])
	assert.Equal(t, "***", afterB["phone"])

	denyUnsub, err := csB.CallTool(ctxB, &sdk.CallToolParams{
		Name:      "unsubscribe",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID},
	})
	require.NoError(t, err)
	require.True(t, denyUnsub.IsError)
	assert.Contains(t, contentText(denyUnsub), "not owned")

	listRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{Name: "list_subscriptions", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.False(t, listRes.IsError)
	var listOut struct {
		Subscriptions []struct {
			ID string `json:"id"`
		} `json:"subscriptions"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(listRes)), &listOut))
	require.Len(t, listOut.Subscriptions, 1)
	assert.Equal(t, subOut.SubscriptionID, listOut.Subscriptions[0].ID)

	_, err = csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}},
	})
	require.NoError(t, err)
	capRes, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{"tables": []string{"public.orders"}},
	})
	require.NoError(t, err)
	require.True(t, capRes.IsError)
	assert.Contains(t, contentText(capRes), "subscription limit")

	unsub, err := csA.CallTool(ctxA, &sdk.CallToolParams{
		Name:      "unsubscribe",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID},
	})
	require.NoError(t, err)
	require.False(t, unsub.IsError)
}

func TestSubscription_RingEvictionAndDrainReset(t *testing.T) {
	t.Setenv("MCP_RING", "ring-secret")
	metrics := observability.NewKaptantoMetrics()
	s, probe := newSubServer(t, []config.MCPAPIKey{
		{Name: "agent", Key: "${MCP_RING}", Tables: []string{"public.orders"}},
	}, 3, 16, metrics)

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

	for i := 1; i <= 5; i++ {
		ev := &event.ChangeEvent{
			Schema:    "public",
			Table:     "orders",
			Operation: event.OpInsert,
			After:     json.RawMessage(`{"id":` + strconv.Itoa(i) + `}`),
			Key:       json.RawMessage(`{"id":` + strconv.Itoa(i) + `}`),
		}
		require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{Seq: uint64(i), Event: ev}))
	}
	assert.GreaterOrEqual(t, testutil.ToFloat64(metrics.MCPEventsDroppedTotal.WithLabelValues("agent")), 2.0)
	assert.GreaterOrEqual(t, testutil.ToFloat64(metrics.MCPEventsBufferedTotal), 5.0)

	drainRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID, "max": 10},
	})
	require.NoError(t, err)
	require.False(t, drainRes.IsError)
	var drainOut struct {
		Events    []map[string]any `json:"events"`
		Dropped   int              `json:"dropped"`
		Remaining int              `json:"remaining"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drainRes)), &drainOut))
	require.Len(t, drainOut.Events, 3)
	assert.Equal(t, 2, drainOut.Dropped)
	assert.Equal(t, 0, drainOut.Remaining)
	assert.Equal(t, float64(3), drainOut.Events[0]["after"].(map[string]any)["id"])

	drain2, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_recent_events",
		Arguments: map[string]any{"subscription_id": subOut.SubscriptionID},
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(drain2)), &drainOut))
	assert.Equal(t, 0, drainOut.Dropped)
	assert.Empty(t, drainOut.Events)
}

func TestSubscription_NudgeDebounceFakeClock(t *testing.T) {
	t.Setenv("MCP_NUDGE", "nudge-secret")
	metrics := observability.NewKaptantoMetrics()
	s, probe := newSubServer(t, []config.MCPAPIKey{
		{Name: "agent", Key: "${MCP_NUDGE}"},
	}, 16, 16, metrics)
	clock := newFakeClock(time.Unix(0, 0))
	s.SetClock(clock)

	var nudgeMu sync.Mutex
	var nudges []string

	ctx := mcp.ContextWithPrincipal(context.Background(), s.Keys()[0])
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := s.SDK().Connect(ctx, t1, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "nudge-client", Version: "v0"}, &sdk.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			nudgeMu.Lock()
			nudges = append(nudges, req.Params.URI)
			nudgeMu.Unlock()
		},
	})
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	subRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
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
	require.NoError(t, cs.Subscribe(ctx, &sdk.SubscribeParams{URI: subOut.ResourceURI}))

	for i := 0; i < 5; i++ {
		ev := &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: event.OpInsert,
			After: json.RawMessage(`{"id":1}`), Key: json.RawMessage(`{"id":1}`),
		}
		require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{Seq: uint64(i + 1), Event: ev}))
	}
	nudgeMu.Lock()
	assert.Empty(t, nudges)
	nudgeMu.Unlock()

	clock.Advance(100 * time.Millisecond)
	require.Eventually(t, func() bool {
		nudgeMu.Lock()
		defer nudgeMu.Unlock()
		return len(nudges) == 1
	}, time.Second, 5*time.Millisecond)
	nudgeMu.Lock()
	assert.Equal(t, []string{subOut.ResourceURI}, nudges)
	nudgeMu.Unlock()
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.MCPNudgesTotal))

	for i := 0; i < 3; i++ {
		ev := &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: event.OpInsert,
			After: json.RawMessage(`{"id":2}`), Key: json.RawMessage(`{"id":2}`),
		}
		require.NoError(t, probe.Deliver(subOut.SubscriptionID, eventlog.LogEntry{Seq: uint64(10 + i), Event: ev}))
	}
	clock.Advance(100 * time.Millisecond)
	require.Eventually(t, func() bool {
		nudgeMu.Lock()
		defer nudgeMu.Unlock()
		return len(nudges) == 2
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, 2.0, testutil.ToFloat64(metrics.MCPNudgesTotal))
}

func TestSubscription_SessionCloseLeakMCP02(t *testing.T) {
	t.Setenv("MCP_LEAK", "leak-secret")
	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	probe := newProbe(rtr)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_LEAK}"}},
		},
		DataDir:  t.TempDir(),
		Auditor:  mcp.NewAuditorWriter(io.Discard, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  metrics,
		Listener: ln,
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	s.SetRouter(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 5*time.Millisecond)

	baseline := probe.ConsumerCount()

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripperBearer{
			base:  http.DefaultTransport,
			token: "leak-secret",
		},
	}
	transport := &sdk.StreamableClientTransport{
		Endpoint:   "http://" + s.Addr().String(),
		HTTPClient: httpClient,
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "leak", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)

	subRes, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "subscribe_to_changes",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, subRes.IsError, contentText(subRes))
	assert.Equal(t, baseline+1, probe.ConsumerCount())

	_ = cs.Close()

	require.Eventually(t, func() bool {
		return probe.ConsumerCount() == baseline && s.ActiveSubscriptionCount() == 0
	}, 3*time.Second, 20*time.Millisecond, "MCP-02: subscriptions must unregister on session close")
}

type roundTripperBearer struct {
	base  http.RoundTripper
	token string
}

func (r roundTripperBearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+r.token)
	return r.base.RoundTrip(req)
}

func TestRouter_Unregister(t *testing.T) {
	rtr := router.NewRouter(&memEventLog{}, 2, nil)
	assert.Equal(t, 0, rtr.ConsumerCount())

	c := &stubConsumer{id: "c1"}
	rtr.Register(c)
	assert.Equal(t, 1, rtr.ConsumerCount())
	assert.True(t, rtr.Unregister("c1"))
	assert.Equal(t, 0, rtr.ConsumerCount())
	assert.False(t, rtr.Unregister("c1"))
}

type stubConsumer struct{ id string }

func (s *stubConsumer) ID() string { return s.id }
func (s *stubConsumer) Deliver(context.Context, eventlog.LogEntry) error { return nil }
