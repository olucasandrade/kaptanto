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

	"github.com/kaptanto/kaptanto/internal/eventlog"
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
type consumerRetryState struct {
	consumer      Consumer
	blockedGroups map[string]*RetryRecord
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
		blockedGroups: make(map[string]*RetryRecord),
	}
	rs.states[id] = s
	return s
}

// AddBlocked registers a RetryRecord under the given groupKey for consumer c.
// This is called by Router.dispatch when initial delivery fails, and is also
// used directly by tests.
func (rs *RetryScheduler) AddBlocked(c Consumer, groupKey string, rec *RetryRecord) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	s := rs.ensureStateLocked(c)
	s.blockedGroups[groupKey] = rec
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
	rec, ok := s.blockedGroups[groupKey]
	if !ok {
		return
	}
	rec.NextRetryAt = time.Now().Add(-time.Second)
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
	_, blocked := s.blockedGroups[groupKey]
	return blocked
}

// Tick iterates all consumers' blocked groups and re-attempts delivery for
// entries whose NextRetryAt is in the past. On success the entry is removed.
// On failure the attempt counter is incremented and NextRetryAt is pushed
// forward. After maxRetries failures the entry is dead-lettered.
func (rs *RetryScheduler) Tick(ctx context.Context) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	now := time.Now()
	for _, s := range rs.states {
		for groupKey, rec := range s.blockedGroups {
			if now.Before(rec.NextRetryAt) {
				continue
			}
			err := s.consumer.Deliver(ctx, rec.Entry)
			if err == nil {
				delete(s.blockedGroups, groupKey)
				continue
			}
			// Permanent error → dead-letter immediately.
			if isPermanentError(err) {
				deadLetter(s, groupKey, rec)
				continue
			}
			rec.Attempts++
			if rec.Attempts >= maxRetries {
				deadLetter(s, groupKey, rec)
				continue
			}
			rec.NextRetryAt = time.Now().Add(NextDelay(rec.Attempts))
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

// deadLetter logs a slog.Error for a dead-lettered entry and removes it from
// the consumer's blocked groups.
func deadLetter(s *consumerRetryState, groupKey string, rec *RetryRecord) {
	slog.Error("router: dead-letter",
		"consumer_id", rec.ConsumerID,
		"event_id", rec.Entry.Event.ID.String(),
		"table", rec.Entry.Event.Table,
		"key", string(rec.Entry.Event.Key),
		"attempts", rec.Attempts,
	)
	delete(s.blockedGroups, groupKey)
}
