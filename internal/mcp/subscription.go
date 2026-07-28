package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/olucasandrade/kaptanto/internal/routing"
)

const (
	toolSubscribeToChanges = "subscribe_to_changes"
	toolUnsubscribe        = "unsubscribe"
	toolListSubscriptions  = "list_subscriptions"
	toolGetRecentEvents    = "get_recent_events"

	subscriptionURIPrefix = "kaptanto://subscriptions/"
	nudgeDebounce         = 100 * time.Millisecond
	defaultDrainMax       = 100
)

// ConsumerRegistry is the subset of *router.Router used by MCP subscriptions.
type ConsumerRegistry interface {
	Register(c router.Consumer)
	Unregister(id string) bool
	ConsumerCount() int
}

// Clock abstracts time for nudge debounce tests.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (cancel func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) (cancel func()) {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// subscribeInput matches routing.MatchConfig JSON shape.
type subscribeInput struct {
	Tables     []string `json:"tables,omitempty" jsonschema:"table globs, e.g. public.orders"`
	Operations []string `json:"operations,omitempty" jsonschema:"insert|update|delete|read"`
	Where      string   `json:"where,omitempty" jsonschema:"row filter expression"`
}

type subscribeOutput struct {
	SubscriptionID string `json:"subscription_id"`
	ResourceURI    string `json:"resource_uri"`
}

type unsubscribeInput struct {
	SubscriptionID string `json:"subscription_id" jsonschema:"subscription id returned by subscribe_to_changes"`
}

type unsubscribeOutput struct {
	OK bool `json:"ok"`
}

type listSubscriptionsInput struct{}

type subscriptionInfo struct {
	ID        string              `json:"id"`
	Match     routing.MatchConfig `json:"match"`
	Buffered  int                 `json:"buffered"`
	Dropped   int                 `json:"dropped"`
	CreatedAt time.Time           `json:"created_at"`
}

type listSubscriptionsOutput struct {
	Subscriptions []subscriptionInfo `json:"subscriptions"`
}

type getRecentEventsInput struct {
	SubscriptionID string `json:"subscription_id" jsonschema:"subscription id returned by subscribe_to_changes"`
	Max            int    `json:"max,omitempty" jsonschema:"max events to drain; default 100"`
}

type getRecentEventsOutput struct {
	Events    []map[string]any `json:"events"`
	Dropped   int              `json:"dropped"`
	Remaining int              `json:"remaining"`
}

// ringBuffer is a fixed-capacity FIFO of ChangeEvent pointers.
type ringBuffer struct {
	slots   []*event.ChangeEvent
	start   int
	length  int
	dropped int
}

func newRingBuffer(cap int) *ringBuffer {
	if cap < 1 {
		cap = DefaultRingSize
	}
	return &ringBuffer{slots: make([]*event.ChangeEvent, cap)}
}

func (r *ringBuffer) push(ev *event.ChangeEvent) (evicted bool) {
	if r.length == len(r.slots) {
		r.slots[r.start] = nil
		r.start = (r.start + 1) % len(r.slots)
		r.length--
		r.dropped++
		evicted = true
	}
	r.slots[(r.start+r.length)%len(r.slots)] = ev
	r.length++
	return evicted
}

func (r *ringBuffer) drain(max int) (events []*event.ChangeEvent, dropped, remaining int) {
	dropped = r.dropped
	r.dropped = 0
	if max < 0 {
		max = 0
	}
	if max > r.length {
		max = r.length
	}
	events = make([]*event.ChangeEvent, max)
	for i := 0; i < max; i++ {
		events[i] = r.slots[r.start]
		r.slots[r.start] = nil
		r.start = (r.start + 1) % len(r.slots)
		r.length--
	}
	return events, dropped, r.length
}

func (r *ringBuffer) buffered() int { return r.length }
func (r *ringBuffer) droppedCount() int {
	return r.dropped
}

// subscription is a bounded ring-buffer router.Consumer (MCP-04).
// Deliver never blocks and never returns an error.
type subscription struct {
	id         string
	keyName    string
	sessionID  string
	acl        *ACL
	match      routing.MatchConfig
	matcher    *routing.Matcher
	resourceURI string
	createdAt  time.Time

	mu     sync.Mutex
	ring   *ringBuffer
	closed bool

	nudgeMu      sync.Mutex
	nudgePending atomic.Bool
	nudgeCancel  func()

	clock    Clock
	nudgeFn  func(uri string) // injectable; default → ResourceUpdated
	onBuffer func()          // metrics: buffered
	onDrop   func()          // metrics: dropped
	onNudge  func()          // metrics: nudge sent
}

var _ router.Consumer = (*subscription)(nil)

func (sub *subscription) ID() string { return sub.id }

// Deliver matches, ring-appends (evicting oldest when full), and schedules a
// debounced resources/updated nudge. Never blocks; always returns nil (MCP-04).
func (sub *subscription) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	if entry.Event == nil {
		return nil
	}
	ok, err := sub.matcher.Match(entry.Event)
	if err != nil || !ok {
		return nil
	}
	qualified := qualifiedName(entry.Event.Schema, entry.Event.Table)
	if !sub.acl.AllowTable(qualified) {
		return nil
	}

	// Shallow copy so drain-time redaction cannot race with later mutations.
	cp := *entry.Event

	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		return nil
	}
	evicted := sub.ring.push(&cp)
	sub.mu.Unlock()

	if sub.onBuffer != nil {
		sub.onBuffer()
	}
	if evicted && sub.onDrop != nil {
		sub.onDrop()
	}
	sub.scheduleNudge()
	return nil
}

func (sub *subscription) scheduleNudge() {
	if sub.nudgePending.Load() {
		return
	}
	sub.nudgeMu.Lock()
	defer sub.nudgeMu.Unlock()
	if sub.nudgePending.Load() {
		return
	}
	sub.nudgePending.Store(true)
	clock := sub.clock
	if clock == nil {
		clock = realClock{}
	}
	sub.nudgeCancel = clock.AfterFunc(nudgeDebounce, func() {
		sub.nudgeMu.Lock()
		sub.nudgePending.Store(false)
		sub.nudgeCancel = nil
		sub.nudgeMu.Unlock()

		sub.mu.Lock()
		closed := sub.closed
		uri := sub.resourceURI
		sub.mu.Unlock()
		if closed {
			return
		}
		if sub.nudgeFn != nil {
			sub.nudgeFn(uri)
		}
		if sub.onNudge != nil {
			sub.onNudge()
		}
	})
}

func (sub *subscription) closeRing() {
	sub.nudgeMu.Lock()
	if sub.nudgeCancel != nil {
		sub.nudgeCancel()
		sub.nudgeCancel = nil
	}
	sub.nudgePending.Store(false)
	sub.nudgeMu.Unlock()

	sub.mu.Lock()
	sub.closed = true
	sub.ring = nil
	sub.mu.Unlock()
}

func (sub *subscription) drain(max int) (events []*event.ChangeEvent, dropped, remaining int) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed || sub.ring == nil {
		return nil, 0, 0
	}
	raw, dropped, remaining := sub.ring.drain(max)
	out := make([]*event.ChangeEvent, 0, len(raw))
	for _, ev := range raw {
		if ev == nil {
			continue
		}
		redacted, ok := sub.acl.Apply(ev)
		if !ok {
			continue
		}
		out = append(out, redacted)
	}
	return out, dropped, remaining
}

func (sub *subscription) snapshot() (buffered, dropped int) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed || sub.ring == nil {
		return 0, 0
	}
	return sub.ring.buffered(), sub.ring.droppedCount()
}

// registerSubscriptionTools wires the four subscription tools.
func (s *Server) registerSubscriptionTools() {
	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolSubscribeToChanges,
		Description: "Subscribe to CDC changes matching tables/operations/where. Returns a subscription_id and resource_uri; drain via get_recent_events. Ephemeral — re-subscribe on reconnect.",
	}, s.handleSubscribeToChanges)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolUnsubscribe,
		Description: "Unregister a subscription owned by this API key and free its ring buffer.",
	}, s.handleUnsubscribe)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolListSubscriptions,
		Description: "List this API key's active MCP subscriptions.",
	}, s.handleListSubscriptions)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolGetRecentEvents,
		Description: "Drain up to max events from a subscription ring (FIFO), applying ACL redaction. Reports and resets the dropped counter.",
	}, s.handleGetRecentEvents)
}

func (s *Server) handleSubscribeToChanges(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in subscribeInput,
) (*sdk.CallToolResult, subscribeOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}
	sessionID := ""
	if req != nil && req.Session != nil {
		sessionID = req.Session.ID()
	}

	out, tables, outcome, err := s.subscribeToChanges(key, sessionID, in)
	s.RecordToolCall(keyName, toolSubscribeToChanges, subscribeParamsSummary(in), tables, outcome, time.Since(start))
	if err != nil {
		return nil, subscribeOutput{}, err
	}
	return nil, out, nil
}

func subscribeParamsSummary(in subscribeInput) []string {
	var p []string
	if len(in.Tables) > 0 {
		p = append(p, "tables")
	}
	if len(in.Operations) > 0 {
		p = append(p, "operations")
	}
	if strings.TrimSpace(in.Where) != "" {
		p = append(p, "where")
	}
	return p
}

func (s *Server) subscribeToChanges(key *ResolvedKey, sessionID string, in subscribeInput) (subscribeOutput, []string, string, error) {
	if key == nil {
		return subscribeOutput{}, nil, OutcomeError, fmt.Errorf("unauthenticated")
	}
	s.subsMu.RLock()
	reg := s.registry
	s.subsMu.RUnlock()
	if reg == nil {
		return subscribeOutput{}, nil, OutcomeError, fmt.Errorf("subscriptions unavailable: router not configured")
	}

	mc := routing.MatchConfig{
		Tables:     append([]string(nil), in.Tables...),
		Operations: append([]string(nil), in.Operations...),
		Where:      in.Where,
	}

	// Validate where/ops before creating anything (invalid where → tool error).
	if _, err := routing.Compile(mc); err != nil {
		return subscribeOutput{}, nil, OutcomeError, err
	}

	if len(mc.Tables) > 0 {
		allowed := key.ACL.FilterTables(mc.Tables)
		if len(allowed) == 0 {
			return subscribeOutput{}, mc.Tables, OutcomeDenied, errDenied
		}
		mc.Tables = allowed
	}

	matcher, err := routing.Compile(mc)
	if err != nil {
		return subscribeOutput{}, nil, OutcomeError, err
	}

	s.subsMu.Lock()
	defer s.subsMu.Unlock()

	count := 0
	for _, sub := range s.subs {
		if sub.keyName == key.Name {
			count++
		}
	}
	maxSubs := s.opts.Config.MaxSubscriptions
	if maxSubs <= 0 {
		maxSubs = DefaultMaxSubscriptions
	}
	if count >= maxSubs {
		return subscribeOutput{}, mc.Tables, OutcomeError, fmt.Errorf("subscription limit reached (%d)", maxSubs)
	}

	ulidStr := ulid.Make().String()
	id := fmt.Sprintf("mcp:%s:%s", key.Name, ulidStr)
	uri := subscriptionURIPrefix + id
	ringSize := s.opts.Config.RingSize
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}

	sub := &subscription{
		id:          id,
		keyName:     key.Name,
		sessionID:   sessionID,
		acl:         key.ACL,
		match:       mc,
		matcher:     matcher,
		resourceURI: uri,
		createdAt:   s.clock().Now(),
		ring:        newRingBuffer(ringSize),
		clock:       s.clock(),
	}
	sub.nudgeFn = func(u string) {
		_ = s.sdk.ResourceUpdated(context.Background(), &sdk.ResourceUpdatedNotificationParams{URI: u})
	}
	if s.metrics != nil {
		m := s.metrics
		keyLabel := key.Name
		sub.onBuffer = func() { m.MCPEventsBufferedTotal.Inc() }
		sub.onDrop = func() { m.MCPEventsDroppedTotal.WithLabelValues(keyLabel).Inc() }
		sub.onNudge = func() { m.MCPNudgesTotal.Inc() }
	}

	s.subs[id] = sub
	if sessionID != "" {
		s.sessionSubs[sessionID] = append(s.sessionSubs[sessionID], id)
	}
	reg.Register(sub)
	if s.metrics != nil {
		s.metrics.MCPSubscriptionsActive.Inc()
	}

	s.sdk.AddResource(&sdk.Resource{
		URI:         uri,
		Name:        "CDC subscription " + id,
		Description: "Bounded ring buffer; drain with get_recent_events. Ephemeral — re-subscribe after reconnect.",
		MIMEType:    "application/json",
	}, s.readSubscriptionResource)

	return subscribeOutput{SubscriptionID: id, ResourceURI: uri}, mc.Tables, OutcomeOK, nil
}

func (s *Server) readSubscriptionResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := ""
	if req != nil && req.Params != nil {
		uri = req.Params.URI
	}
	id := strings.TrimPrefix(uri, subscriptionURIPrefix)
	s.subsMu.RLock()
	sub := s.subs[id]
	s.subsMu.RUnlock()
	if sub == nil {
		return nil, fmt.Errorf("subscription not found")
	}
	// MCP-01: subscription resources are per-key; foreign keys get the same
	// "not found" response as an unknown id (no existence oracle).
	key := PrincipalFromContext(ctx)
	if key == nil || sub.keyName != key.Name {
		return nil, fmt.Errorf("subscription not found")
	}
	buffered, dropped := sub.snapshot()
	text := fmt.Sprintf(`{"subscription_id":%q,"buffered":%d,"dropped":%d,"hint":"use get_recent_events to drain"}`, id, buffered, dropped)
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
		URI:      uri,
		MIMEType: "application/json",
		Text:     text,
	}}}, nil
}

func (s *Server) handleUnsubscribe(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	in unsubscribeInput,
) (*sdk.CallToolResult, unsubscribeOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}
	outcome, err := s.unsubscribe(key, in.SubscriptionID)
	s.RecordToolCall(keyName, toolUnsubscribe, []string{"subscription_id"}, nil, outcome, time.Since(start))
	if err != nil {
		return nil, unsubscribeOutput{}, err
	}
	return nil, unsubscribeOutput{OK: true}, nil
}

func (s *Server) unsubscribe(key *ResolvedKey, subscriptionID string) (string, error) {
	if key == nil {
		return OutcomeError, fmt.Errorf("unauthenticated")
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return OutcomeError, fmt.Errorf("subscription_id is required")
	}

	s.subsMu.Lock()
	sub, ok := s.subs[subscriptionID]
	if !ok {
		s.subsMu.Unlock()
		return OutcomeError, fmt.Errorf("subscription not found")
	}
	if sub.keyName != key.Name {
		s.subsMu.Unlock()
		return OutcomeDenied, fmt.Errorf("subscription not found")
	}
	s.removeSubscriptionLocked(sub)
	s.subsMu.Unlock()
	return OutcomeOK, nil
}

func (s *Server) handleListSubscriptions(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	_ listSubscriptionsInput,
) (*sdk.CallToolResult, listSubscriptionsOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}
	out, outcome, err := s.listSubscriptions(key)
	s.RecordToolCall(keyName, toolListSubscriptions, nil, nil, outcome, time.Since(start))
	if err != nil {
		return nil, listSubscriptionsOutput{}, err
	}
	return nil, out, nil
}

func (s *Server) listSubscriptions(key *ResolvedKey) (listSubscriptionsOutput, string, error) {
	if key == nil {
		return listSubscriptionsOutput{}, OutcomeError, fmt.Errorf("unauthenticated")
	}
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	out := listSubscriptionsOutput{Subscriptions: []subscriptionInfo{}}
	for _, sub := range s.subs {
		if sub.keyName != key.Name {
			continue
		}
		buffered, dropped := sub.snapshot()
		out.Subscriptions = append(out.Subscriptions, subscriptionInfo{
			ID:        sub.id,
			Match:     sub.match,
			Buffered:  buffered,
			Dropped:   dropped,
			CreatedAt: sub.createdAt,
		})
	}
	return out, OutcomeOK, nil
}

func (s *Server) handleGetRecentEvents(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	in getRecentEventsInput,
) (*sdk.CallToolResult, getRecentEventsOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}
	out, outcome, err := s.getRecentEvents(key, in)
	s.RecordToolCall(keyName, toolGetRecentEvents, []string{"subscription_id"}, nil, outcome, time.Since(start))
	if err != nil {
		return nil, getRecentEventsOutput{}, err
	}
	return nil, out, nil
}

func (s *Server) getRecentEvents(key *ResolvedKey, in getRecentEventsInput) (getRecentEventsOutput, string, error) {
	if key == nil {
		return getRecentEventsOutput{}, OutcomeError, fmt.Errorf("unauthenticated")
	}
	id := strings.TrimSpace(in.SubscriptionID)
	if id == "" {
		return getRecentEventsOutput{}, OutcomeError, fmt.Errorf("subscription_id is required")
	}
	max := in.Max
	if max <= 0 {
		max = defaultDrainMax
	}

	s.subsMu.RLock()
	sub, ok := s.subs[id]
	s.subsMu.RUnlock()
	if !ok {
		return getRecentEventsOutput{}, OutcomeError, fmt.Errorf("subscription not found")
	}
	if sub.keyName != key.Name {
		return getRecentEventsOutput{}, OutcomeDenied, fmt.Errorf("subscription not found")
	}
	events, dropped, remaining := sub.drain(max)
	outEvents := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		outEvents = append(outEvents, m)
	}
	return getRecentEventsOutput{Events: outEvents, Dropped: dropped, Remaining: remaining}, OutcomeOK, nil
}

// SetRouter attaches the pipeline router used to register ring consumers and
// the always-on recent-event index consumer (MCP-04: only while enabled with
// ≥1 API key — which New already enforces). Safe to call after New; required
// before subscribe_to_changes / get_event_by_id succeed.
func (s *Server) SetRouter(r ConsumerRegistry) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	if s.recent != nil && s.registry != nil {
		s.registry.Unregister(s.recent.ID())
	}
	s.registry = r
	if r != nil {
		s.startRecentIndexLocked()
	} else if s.recent != nil {
		s.recent.close()
		s.recent = nil
	}
}

// SetClock replaces the wall clock (tests: fake clock for nudge debounce).
func (s *Server) SetClock(c Clock) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	s.clockFn = c
}

func (s *Server) clock() Clock {
	if s.clockFn != nil {
		return s.clockFn
	}
	return realClock{}
}

// ConsumerCount exposes the router consumer count for MCP-02 leak tests.
func (s *Server) ConsumerCount() int {
	s.subsMu.RLock()
	reg := s.registry
	s.subsMu.RUnlock()
	if reg == nil {
		return 0
	}
	return reg.ConsumerCount()
}

// ActiveSubscriptionCount returns the number of live MCP subscriptions.
func (s *Server) ActiveSubscriptionCount() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return len(s.subs)
}

func (s *Server) onSessionInitialized(_ context.Context, req *sdk.InitializedRequest) {
	if req == nil || req.Session == nil {
		return
	}
	ss := req.Session
	sid := ss.ID()
	go func() {
		_ = ss.Wait()
		s.cleanupSession(sid)
	}()
}

func (s *Server) cleanupSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.subsMu.Lock()
	ids := append([]string(nil), s.sessionSubs[sessionID]...)
	delete(s.sessionSubs, sessionID)
	for _, id := range ids {
		if sub, ok := s.subs[id]; ok {
			s.removeSubscriptionLocked(sub)
		}
	}
	s.subsMu.Unlock()
}

// removeSubscriptionLocked unregisters from the router, frees the ring, and
// drops SDK resource. Caller must hold s.subsMu.
func (s *Server) removeSubscriptionLocked(sub *subscription) {
	if sub == nil {
		return
	}
	id := sub.id
	if _, ok := s.subs[id]; !ok {
		return
	}
	delete(s.subs, id)
	if sid := sub.sessionID; sid != "" {
		kept := s.sessionSubs[sid][:0]
		for _, x := range s.sessionSubs[sid] {
			if x != id {
				kept = append(kept, x)
			}
		}
		if len(kept) == 0 {
			delete(s.sessionSubs, sid)
		} else {
			s.sessionSubs[sid] = kept
		}
	}
	sub.closeRing()
	if s.registry != nil {
		s.registry.Unregister(id)
	}
	if s.metrics != nil {
		s.metrics.MCPSubscriptionsActive.Dec()
	}
	s.sdk.RemoveResources(sub.resourceURI)
}

func (s *Server) cleanupAllSubscriptions() {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for _, sub := range s.subs {
		sub.closeRing()
		if s.registry != nil {
			s.registry.Unregister(sub.id)
		}
		s.sdk.RemoveResources(sub.resourceURI)
	}
	if s.metrics != nil && len(s.subs) > 0 {
		s.metrics.MCPSubscriptionsActive.Sub(float64(len(s.subs)))
	}
	s.subs = make(map[string]*subscription)
	s.sessionSubs = make(map[string][]string)
	s.stopRecentIndexLocked()
}

func (s *Server) allowResourceSubscribe(ctx context.Context, req *sdk.SubscribeRequest) error {
	if req == nil || req.Params == nil {
		return fmt.Errorf("missing subscribe params")
	}
	if !strings.HasPrefix(req.Params.URI, subscriptionURIPrefix) {
		return fmt.Errorf("unknown resource")
	}
	return s.checkSubscriptionOwnership(ctx, req.Params.URI)
}

func (s *Server) allowResourceUnsubscribe(ctx context.Context, req *sdk.UnsubscribeRequest) error {
	if req == nil || req.Params == nil {
		return fmt.Errorf("missing unsubscribe params")
	}
	if !strings.HasPrefix(req.Params.URI, subscriptionURIPrefix) {
		return fmt.Errorf("unknown resource")
	}
	return s.checkSubscriptionOwnership(ctx, req.Params.URI)
}

// checkSubscriptionOwnership enforces MCP-01 on resource subscribe/unsubscribe:
// the requesting key must own the subscription. Foreign or unknown ids both
// yield "unknown resource" so keys cannot probe each other's subscription ids.
func (s *Server) checkSubscriptionOwnership(ctx context.Context, uri string) error {
	id := strings.TrimPrefix(uri, subscriptionURIPrefix)
	s.subsMu.RLock()
	sub := s.subs[id]
	s.subsMu.RUnlock()
	if sub == nil {
		return fmt.Errorf("unknown resource")
	}
	key := PrincipalFromContext(ctx)
	if key == nil || sub.keyName != key.Name {
		return fmt.Errorf("unknown resource")
	}
	return nil
}
