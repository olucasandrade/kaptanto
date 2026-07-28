package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

const (
	toolGetEventByID = "get_event_by_id"

	// DefaultRecentIndexSize is the fixed capacity of the ULID→event index
	// (MCP-04: bounded memory; no EventLog scan).
	DefaultRecentIndexSize = 10000

	// recentConsumerID is the always-on MCP-internal router.Consumer that feeds
	// the recent-event index. Not session-scoped.
	recentConsumerID = "mcp:internal:recent"
)

type getEventByIDInput struct {
	ID string `json:"id" jsonschema:"event ULID returned in ChangeEvent.id"`
}

type getEventByIDOutput struct {
	Event map[string]any `json:"event"`
}

// recentIndex is a fixed-capacity FIFO ULID→event map. Oldest entries are
// evicted when full. Lookups are O(1); there is no EventLog scan path.
type recentIndex struct {
	mu    sync.RWMutex
	cap   int
	slots []string // ring of ULID strings
	start int
	length int
	byID  map[string]*event.ChangeEvent
}

func newRecentIndex(capacity int) *recentIndex {
	if capacity < 1 {
		capacity = DefaultRecentIndexSize
	}
	return &recentIndex{
		cap:   capacity,
		slots: make([]string, capacity),
		byID:  make(map[string]*event.ChangeEvent, capacity),
	}
}

func (idx *recentIndex) len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.length
}

func (idx *recentIndex) put(ev *event.ChangeEvent) {
	if ev == nil {
		return
	}
	id := ev.ID.String()
	if id == "" || id == "00000000000000000000000000" {
		return
	}
	cp := *ev

	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, exists := idx.byID[id]; exists {
		idx.byID[id] = &cp
		return
	}
	if idx.length == idx.cap {
		old := idx.slots[idx.start]
		delete(idx.byID, old)
		idx.slots[idx.start] = ""
		idx.start = (idx.start + 1) % idx.cap
		idx.length--
	}
	idx.slots[(idx.start+idx.length)%idx.cap] = id
	idx.byID[id] = &cp
	idx.length++
}

func (idx *recentIndex) get(id string) (*event.ChangeEvent, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ev, ok := idx.byID[id]
	if !ok || ev == nil {
		return nil, false
	}
	cp := *ev
	return &cp, true
}

func (idx *recentIndex) clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.byID = make(map[string]*event.ChangeEvent, idx.cap)
	idx.slots = make([]string, idx.cap)
	idx.start = 0
	idx.length = 0
}

// recentConsumer is a lightweight always-on router.Consumer that indexes every
// delivered event by ULID. Deliver never blocks and never returns an error (MCP-04).
type recentConsumer struct {
	idx *recentIndex
}

var _ router.Consumer = (*recentConsumer)(nil)

func newRecentConsumer(capacity int) *recentConsumer {
	return &recentConsumer{idx: newRecentIndex(capacity)}
}

func (c *recentConsumer) ID() string { return recentConsumerID }

func (c *recentConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	if entry.Event == nil || c == nil || c.idx == nil {
		return nil
	}
	c.idx.put(entry.Event)
	return nil
}

func (c *recentConsumer) close() {
	if c == nil || c.idx == nil {
		return
	}
	c.idx.clear()
}

func (s *Server) recentCap() int {
	if s.opts.RecentIndexSize > 0 {
		return s.opts.RecentIndexSize
	}
	return DefaultRecentIndexSize
}

// startRecentIndexLocked registers the always-on index consumer. Caller holds
// s.subsMu. Only runs when the server is enabled with ≥1 API key (guaranteed by New).
func (s *Server) startRecentIndexLocked() {
	if s.registry == nil || len(s.keys) == 0 {
		return
	}
	if s.recent != nil {
		s.registry.Register(s.recent)
		return
	}
	s.recent = newRecentConsumer(s.recentCap())
	s.registry.Register(s.recent)
}

// stopRecentIndexLocked unregisters and clears the index. Caller holds s.subsMu.
func (s *Server) stopRecentIndexLocked() {
	if s.recent == nil {
		return
	}
	if s.registry != nil {
		s.registry.Unregister(s.recent.ID())
	}
	s.recent.close()
	s.recent = nil
}

// RecentIndexActive reports whether the internal recent-event consumer is registered.
func (s *Server) RecentIndexActive() bool {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return s.recent != nil
}

// RecentIndexLen returns the number of events currently held (tests).
func (s *Server) RecentIndexLen() int {
	s.subsMu.RLock()
	r := s.recent
	s.subsMu.RUnlock()
	if r == nil || r.idx == nil {
		return 0
	}
	return r.idx.len()
}

// RecentIndexCapacity returns the configured index capacity.
func (s *Server) RecentIndexCapacity() int {
	return s.recentCap()
}

func (s *Server) registerRecentTools() {
	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolGetEventByID,
		Description: "Look up a ChangeEvent by ULID in the bounded recent-event index (last N events). No EventLog scan. ACL + redaction applied. Misses suggest get_recent_events on a subscription.",
	}, s.handleGetEventByID)
}

func (s *Server) handleGetEventByID(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	in getEventByIDInput,
) (*sdk.CallToolResult, getEventByIDOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}

	out, tables, outcome, err := s.getEventByID(key, in.ID)
	s.RecordToolCall(keyName, toolGetEventByID, []string{"id"}, tables, outcome, time.Since(start))
	if err != nil {
		return nil, getEventByIDOutput{}, err
	}
	return nil, out, nil
}

func (s *Server) getEventByID(key *ResolvedKey, id string) (getEventByIDOutput, []string, string, error) {
	if key == nil {
		return getEventByIDOutput{}, nil, OutcomeError, fmt.Errorf("unauthenticated")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return getEventByIDOutput{}, nil, OutcomeError, fmt.Errorf("id is required")
	}

	s.subsMu.RLock()
	r := s.recent
	capN := s.recentCap()
	s.subsMu.RUnlock()
	if r == nil || r.idx == nil {
		return getEventByIDOutput{}, nil, OutcomeError, fmt.Errorf(
			"event not in recent index (last %d events); use get_recent_events on a subscription", capN)
	}

	ev, ok := r.idx.get(id)
	if !ok {
		return getEventByIDOutput{}, nil, OutcomeError, fmt.Errorf(
			"event not in recent index (last %d events); use get_recent_events on a subscription", capN)
	}

	qualified := qualifiedName(ev.Schema, ev.Table)
	redacted, allowed := key.ACL.Apply(ev)
	if !allowed {
		return getEventByIDOutput{}, []string{qualified}, OutcomeDenied, fmt.Errorf(
			"event not in recent index (last %d events); use get_recent_events on a subscription", capN)
	}

	b, err := json.Marshal(redacted)
	if err != nil {
		return getEventByIDOutput{}, []string{qualified}, OutcomeError, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return getEventByIDOutput{}, []string{qualified}, OutcomeError, err
	}
	return getEventByIDOutput{Event: m}, []string{qualified}, OutcomeOK, nil
}
