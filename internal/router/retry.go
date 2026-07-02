// Package router — retry scheduler for RTR-05.
//
// RetryScheduler re-attempts blocked message group entries on an exponential
// backoff schedule (1s, 5s, 30s, 2min, 10min plateau). After maxRetries failed
// attempts the entry is dead-lettered: logged at slog.Error and removed from
// blockedGroups so it no longer blocks subsequent events for that key.
package router

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

// retryDelays defines the exponential backoff schedule for delivery retries.
// When the attempt index exceeds the last element, the last element is used
// (plateau behaviour).
var retryDelays = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// RetryDelays is the exported alias used by tests.
var RetryDelays = retryDelays

// maxRetries is the number of failed delivery attempts before an entry is
// dead-lettered.
const maxRetries = 15

// retryTickInterval is how often RetryScheduler.Run fires its internal tick.
const retryTickInterval = 1 * time.Second

// NextDelay returns the backoff duration for the given attempt index.
// If attempt >= len(retryDelays) the last (maximum) duration is returned.
func NextDelay(attempt int) time.Duration {
	if attempt >= len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt]
}

// isPermanentError reports whether err is a permanent delivery error that
// should trigger immediate dead-lettering without waiting for maxRetries.
func isPermanentError(err error) bool {
	return isErr(err, io.ErrClosedPipe) || isErr(err, os.ErrDeadlineExceeded)
}

// isErr is a helper that checks errors.Is without importing "errors" at top.
func isErr(err, target error) bool {
	// Inline unwrap loop avoids a circular import risk and keeps the function
	// readable. errors.Is does the same thing.
	for {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}

// RetryRecord is a blocked message group entry that holds the original event,
// the number of delivery attempts, and the time after which a retry is allowed.
//
// It is exported so that test helpers outside the package can construct and
// inspect records via RetryScheduler.AddBlocked and RetryScheduler.BlockedCount.
type RetryRecord struct {
	Entry       eventlog.LogEntry
	Attempts    int
	NextRetryAt time.Time
	ConsumerID  string
}

// consumerRetryState is the per-consumer blocked map used by RetryScheduler.
// Each groupKey maps to an ordered queue of RetryRecords. The head of the queue
// (index 0) is the record currently being retried; subsequent entries are
// follow-on events that arrived while the group was blocked. They are queued in
// arrival order and delivered head-first to preserve RTR-04 per-key ordering.
type consumerRetryState struct {
	consumer      Consumer
	blockedGroups map[string][]*RetryRecord
}

// RetryScheduler manages retry state for an arbitrary set of consumers.
// It is intentionally decoupled from Router so that it can be unit-tested
// without a live EventLog.
//
// All exported methods are safe for concurrent use. The internal mu guards
// the states map, which is read by Router.dispatch (via IsBlocked/AddBlocked)
// and written by Tick (via Run goroutine).
type RetryScheduler struct {
	mu     sync.Mutex
	states map[string]*consumerRetryState // key = consumer.ID()
}

// NewRetryScheduler creates a new, empty RetryScheduler.
func NewRetryScheduler() *RetryScheduler {
	return &RetryScheduler{states: make(map[string]*consumerRetryState)}
}

// ensureStateLocked returns the consumerRetryState for c, creating it if
// absent. Caller must hold rs.mu.
func (rs *RetryScheduler) ensureStateLocked(c Consumer) *consumerRetryState {
	id := c.ID()
	if s, ok := rs.states[id]; ok {
		return s
	}
	s := &consumerRetryState{
		consumer:      c,
		blockedGroups: make(map[string][]*RetryRecord),
	}
	rs.states[id] = s
	return s
}

// AddBlocked enqueues a RetryRecord for consumer c under the given groupKey.
//
// - First failure: creates a new queue with rec as the head (retry candidate).
// - Follow-on entries while already blocked: appends rec to the existing queue
//   so that all events for the key are preserved in order (RTR-04).
//
// The head of the queue is the record being actively retried by Tick; only
// after the head succeeds does Tick promote the next entry to head.
func (rs *RetryScheduler) AddBlocked(c Consumer, groupKey string, rec *RetryRecord) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	s := rs.ensureStateLocked(c)
	s.blockedGroups[groupKey] = append(s.blockedGroups[groupKey], rec)
}

// BlockedCount returns the number of blocked groups for consumer c.
// Used by tests to assert dead-lettering cleared the map.
func (rs *RetryScheduler) BlockedCount(c Consumer) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	id := c.ID()
	s, ok := rs.states[id]
	if !ok {
		return 0
	}
	return len(s.blockedGroups)
}

// ForceRetryNow sets nextRetryAt to the past for a given consumer + groupKey
// so the next Tick call will attempt delivery immediately. Used only in tests.
func (rs *RetryScheduler) ForceRetryNow(c Consumer, groupKey string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	id := c.ID()
	s, ok := rs.states[id]
	if !ok {
		return
	}
	queue, ok := s.blockedGroups[groupKey]
	if !ok || len(queue) == 0 {
		return
	}
	queue[0].NextRetryAt = time.Now().Add(-time.Second)
}

// HasBlocked reports whether any consumer currently has at least one blocked
// message group. Router.dispatch uses this as a cheap pre-check: when false,
// the groupKey string conversion and per-consumer IsBlocked calls are skipped
// entirely, eliminating an allocation on the hot path for the common case.
func (rs *RetryScheduler) HasBlocked() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, s := range rs.states {
		if len(s.blockedGroups) > 0 {
			return true
		}
	}
	return false
}

// IsBlocked reports whether the given (consumerID, groupKey) pair is currently
// in the blocked state managed by this RetryScheduler. Router.dispatch calls
// this to skip events whose message group is awaiting a retry attempt.
func (rs *RetryScheduler) IsBlocked(consumerID, groupKey string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	s, ok := rs.states[consumerID]
	if !ok {
		return false
	}
	return len(s.blockedGroups[groupKey]) > 0
}

// Tick re-attempts delivery for every blocked group whose head entry's
// NextRetryAt is in the past.
//
// Delivery happens with rs.mu RELEASED: Consumer.Deliver can block for a full
// network timeout, and Router.dispatch takes rs.mu on every event via
// HasBlocked/IsBlocked — holding the lock across Deliver would stall all 64
// partition goroutines behind one slow retry (RTR-04 isolation).
//
// Once a group's head succeeds, the remaining queue drains inline until the
// first failure or ctx cancellation, so a group that accumulated follow-on
// events during an outage recovers in one Tick instead of one entry per tick.
// Per-key ordering is preserved: entries are always delivered head-first.
//
// On failure the attempt counter on the head is incremented and NextRetryAt is
// pushed forward. After maxRetries failures the head entry is dead-lettered and
// the next entry (if any) becomes the new head. Tick is not safe for
// concurrent invocation with itself; Run is the only production caller.
func (rs *RetryScheduler) Tick(ctx context.Context) {
	now := time.Now()

	// Phase 1: snapshot due (consumer state, groupKey) pairs under the lock.
	type dueGroup struct {
		state    *consumerRetryState
		groupKey string
	}
	rs.mu.Lock()
	var due []dueGroup
	for _, s := range rs.states {
		for groupKey, queue := range s.blockedGroups {
			if len(queue) == 0 {
				delete(s.blockedGroups, groupKey)
				continue
			}
			if now.Before(queue[0].NextRetryAt) {
				continue
			}
			due = append(due, dueGroup{state: s, groupKey: groupKey})
		}
	}
	rs.mu.Unlock()

	// Phase 2: deliver outside the lock, one group at a time.
	for _, dg := range due {
		if ctx.Err() != nil {
			return
		}
		rs.drainGroup(ctx, dg.state, dg.groupKey)
	}
}

// drainGroup delivers queued records for one blocked group head-first until
// the first transient failure, an empty queue, or ctx cancellation. rs.mu is
// held only to read the current head and to apply each delivery outcome —
// never across Deliver.
func (rs *RetryScheduler) drainGroup(ctx context.Context, s *consumerRetryState, groupKey string) {
	for ctx.Err() == nil {
		rs.mu.Lock()
		queue := s.blockedGroups[groupKey]
		if len(queue) == 0 {
			delete(s.blockedGroups, groupKey)
			rs.mu.Unlock()
			return
		}
		rec := queue[0]
		if time.Now().Before(rec.NextRetryAt) {
			rs.mu.Unlock()
			return
		}
		rs.mu.Unlock()

		err := s.consumer.Deliver(ctx, rec.Entry)

		rs.mu.Lock()
		// Re-read: AddBlocked may have appended follow-ons while unlocked.
		// Appends never touch the head, so rec is still queue[0].
		queue = s.blockedGroups[groupKey]
		if len(queue) == 0 || queue[0] != rec {
			// Head changed underneath us (concurrent Tick — unsupported, but
			// don't corrupt the queue if it happens).
			rs.mu.Unlock()
			return
		}
		switch {
		case err == nil:
			// Head delivered — pop it and keep draining.
			if len(queue) == 1 {
				delete(s.blockedGroups, groupKey)
			} else {
				queue[1].NextRetryAt = time.Now()
				s.blockedGroups[groupKey] = queue[1:]
			}
			rs.mu.Unlock()
		case isPermanentError(err):
			// Dead-letter the head; next entry becomes eligible immediately.
			deadLetterHead(s, groupKey, queue)
			rs.mu.Unlock()
		default:
			rec.Attempts++
			if rec.Attempts >= maxRetries {
				deadLetterHead(s, groupKey, queue)
				rs.mu.Unlock()
				continue
			}
			rec.NextRetryAt = time.Now().Add(NextDelay(rec.Attempts))
			rs.mu.Unlock()
			return
		}
	}
}

// Run starts a ticker that calls Tick at retryTickInterval until ctx is Done.
func (rs *RetryScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(retryTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.Tick(ctx)
		}
	}
}

// deadLetterHead logs a slog.Error for the head entry of a blocked group and
// pops it. If the queue has more entries the next one becomes the new head and
// is made immediately eligible (NextRetryAt = now). If the queue is now empty
// the group key is removed from blockedGroups.
func deadLetterHead(s *consumerRetryState, groupKey string, queue []*RetryRecord) {
	rec := queue[0]
	// Log the ULID and idempotency key, never the raw PK: Event.Key holds row
	// PK values, and natural keys (emails, account numbers) must not leak
	// into log pipelines.
	slog.Error("router: dead-letter",
		"consumer_id", rec.ConsumerID,
		"event_id", rec.Entry.Event.ID.String(),
		"table", rec.Entry.Event.Table,
		"idempotency_key", rec.Entry.Event.IdempotencyKey,
		"attempts", rec.Attempts,
	)
	if len(queue) == 1 {
		delete(s.blockedGroups, groupKey)
	} else {
		queue[1].NextRetryAt = time.Now()
		s.blockedGroups[groupKey] = queue[1:]
	}
}
