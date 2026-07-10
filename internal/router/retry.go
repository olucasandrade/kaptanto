// Package router — retry scheduler for RTR-05.
//
// RetryScheduler re-attempts blocked message group entries on an exponential
// backoff schedule (1s, 5s, 30s, 2min, 10min plateau). After maxRetries failed
// attempts (or on a PermanentDeliveryError) the entry is dead-lettered: written
// to the configured dlq.Store when present (DLQ-01), otherwise logged at
// slog.Error and removed from blockedGroups so it no longer blocks subsequent
// events for that key.
//
// RTR-06 — cursor floor: RetryScheduler's blockedGroups queue is memory-only.
// Router must never persist a consumer's cursor past the lowest seq of a
// queued-but-undelivered record for that consumer+partition, or a crash while
// a group is blocked would permanently skip every queued follow-on (the
// persisted cursor would already be past them, and no retry state survives
// the restart). RetryScheduler tracks this floor per (consumer, partition)
// and exposes it via Floor(); Router.dispatchUpdateCursor caps every
// SaveCursor call at min(nextSeq, floor). See Floor, AddBlocked, and
// OnFloorReleased.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
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

// maxReasonBytes is the maximum length of a DLQ Reason string built from LastErr.
const maxReasonBytes = 1024

// NextDelay returns the backoff duration for the given attempt index.
// If attempt >= len(retryDelays) the last (maximum) duration is returned.
func NextDelay(attempt int) time.Duration {
	if attempt >= len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt]
}

// JitteredDelay returns NextDelay(attempt) scaled by full jitter: a uniform
// random duration in (0, base]. Full jitter decorrelates retry storms when an
// endpoint outage blocks many keys at once. The 1s scheduler tick is the
// effective floor. Uses math/rand/v2 (no seeding needed).
func JitteredDelay(attempt int) time.Duration {
	base := NextDelay(attempt)
	if base <= 0 {
		return 1
	}
	return time.Duration(rand.Int64N(int64(base))) + 1
}

// isPermanentError reports whether err is a permanent delivery error that
// should trigger immediate dead-lettering without waiting for maxRetries.
// It recognizes PermanentDeliveryError implementors via the unwrap chain, plus
// the historical io.ErrClosedPipe / os.ErrDeadlineExceeded sentinels.
func isPermanentError(err error) bool {
	if err == nil {
		return false
	}
	var perm PermanentDeliveryError
	if errors.As(err, &perm) {
		return true
	}
	return errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrDeadlineExceeded)
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
	// LastErr is the most recent delivery error observed by drainGroup. It
	// becomes the DLQ Entry.Reason when the record is dead-lettered.
	LastErr error
}

// consumerRetryState is the per-consumer blocked map used by RetryScheduler.
// Each groupKey maps to an ordered queue of RetryRecords. The head of the queue
// (index 0) is the record currently being retried; subsequent entries are
// follow-on events that arrived while the group was blocked. They are queued in
// arrival order and delivered head-first to preserve RTR-04 per-key ordering.
type consumerRetryState struct {
	consumer      Consumer
	blockedGroups map[string][]*RetryRecord

	// floors tracks, per partition, the lowest seq among the head records of
	// this consumer's currently blocked groups on that partition (RTR-06).
	// A groupKey's queue always lives entirely on one partition — keys hash
	// to a partition deterministically (same FNV-1a scheme as EventLog and
	// WatermarkChecker, BKF-02) — so only the head record of each group
	// (the lowest-seq, still-undelivered record in that group) can set the
	// partition minimum; follow-on appends never lower it because they
	// arrive in increasing-seq order behind an existing head.
	//
	// Absence of a key means "no floor": no blocked group exists for that
	// partition, so the persisted cursor may advance freely.
	floors map[uint32]uint64
}

// RetryScheduler manages retry state for an arbitrary set of consumers.
// It is intentionally decoupled from Router so that it can be unit-tested
// without a live EventLog.
//
// All exported methods are safe for concurrent use. The internal mu guards
// the states map, which is read by Router.dispatch (via IsBlocked/AddBlocked)
// and written by Tick (via Run goroutine).
//
// Lock ordering (RTR-06): RetryScheduler never calls back into Router while
// holding rs.mu — Deliver is always invoked with rs.mu released (see Tick),
// and OnFloorReleased is invoked after rs.mu is released too. This makes it
// safe for Router to call RetryScheduler methods (AddBlocked, IsBlocked,
// Floor) while holding Router.mu, which is the existing, established order
// (see Router.dispatch): Router.mu is always acquired first, RetryScheduler.mu
// second, never the reverse.
type RetryScheduler struct {
	mu     sync.Mutex
	states map[string]*consumerRetryState // key = consumer.ID()

	// dlq is the optional dead-letter store. nil disables persistence and
	// keeps the historical log-and-drop dead-letter path.
	dlq dlq.Store

	// metrics, when non-nil, receives dlq_events_total / dlq_write_failures_total.
	metrics *observability.KaptantoMetrics

	// OnFloorReleased, if set, is called after a blocked group's head record
	// is popped (delivered or dead-lettered) and the floor for
	// (consumerID, partitionID) has been recomputed. ok reports whether a
	// floor still applies (another blocked group remains on that partition);
	// when ok is false, floor is meaningless and the caller may persist any
	// cursor value. Router wires this to re-persist the released cursor
	// position so a partition that goes quiet after a group drains doesn't
	// leave the cursor pinned at a stale floor. Called with rs.mu released.
	OnFloorReleased func(consumerID string, partitionID uint32, floor uint64, ok bool)
}

// NewRetryScheduler creates a new, empty RetryScheduler.
func NewRetryScheduler() *RetryScheduler {
	return &RetryScheduler{states: make(map[string]*consumerRetryState)}
}

// SetDLQ wires a dead-letter store into the scheduler. A nil store disables
// DLQ persistence (log-and-drop). Safe for concurrent use; intended for root
// command wiring before Run.
func (rs *RetryScheduler) SetDLQ(store dlq.Store) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.dlq = store
}

// SetMetrics wires Prometheus collectors for DLQ write success/failure.
// A nil metrics value disables increments. Safe for concurrent use.
func (rs *RetryScheduler) SetMetrics(m *observability.KaptantoMetrics) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.metrics = m
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
//   - First failure: creates a new queue with rec as the head (retry candidate).
//   - Follow-on entries while already blocked: appends rec to the existing queue
//     so that all events for the key are preserved in order (RTR-04).
//
// The head of the queue is the record being actively retried by Tick; only
// after the head succeeds does Tick promote the next entry to head.
//
// AddBlocked also maintains the per-partition floor (RTR-06): a brand-new
// blocked group's record is a new head, so it can only lower the floor for
// rec.Entry.PartitionID. A follow-on append never changes the floor — it
// arrives with a higher seq than the existing head and sits behind it in the
// queue.
func (rs *RetryScheduler) AddBlocked(c Consumer, groupKey string, rec *RetryRecord) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	s := rs.ensureStateLocked(c)
	_, existed := s.blockedGroups[groupKey]
	s.blockedGroups[groupKey] = append(s.blockedGroups[groupKey], rec)
	if !existed {
		rs.lowerFloorLocked(s, rec.Entry.PartitionID, rec.Entry.Seq)
	}
}

// lowerFloorLocked sets floors[partitionID] = seq if seq is lower than the
// current floor, or no floor is set yet. Caller must hold rs.mu.
func (rs *RetryScheduler) lowerFloorLocked(s *consumerRetryState, partitionID uint32, seq uint64) {
	if s.floors == nil {
		s.floors = make(map[uint32]uint64)
	}
	cur, ok := s.floors[partitionID]
	if !ok || seq < cur {
		s.floors[partitionID] = seq
	}
}

// recomputeFloorLocked recalculates floors[partitionID] for consumer state s
// by scanning the current head of every blocked group that lives on that
// partition. Called after a head record is popped (delivered or
// dead-lettered): popping can only raise the floor, never lower it, but the
// new value can't be derived incrementally because a different group's head
// may now be the partition minimum. Caller must hold rs.mu.
func (rs *RetryScheduler) recomputeFloorLocked(s *consumerRetryState, partitionID uint32) {
	min := uint64(0)
	found := false
	for _, queue := range s.blockedGroups {
		if len(queue) == 0 {
			continue
		}
		head := queue[0]
		if head.Entry.PartitionID != partitionID {
			continue
		}
		if !found || head.Entry.Seq < min {
			min = head.Entry.Seq
			found = true
		}
	}
	if !found {
		delete(s.floors, partitionID)
		return
	}
	if s.floors == nil {
		s.floors = make(map[uint32]uint64)
	}
	s.floors[partitionID] = min
}

// afterPopLocked recomputes the floor for (s, partitionID) after a head
// record was popped, and returns a notify closure that invokes
// OnFloorReleased with the resulting floor. The closure must be called only
// after rs.mu is released — never while holding it — matching the same
// off-lock discipline as Deliver in drainGroup. Caller must hold rs.mu when
// calling afterPopLocked itself.
func (rs *RetryScheduler) afterPopLocked(s *consumerRetryState, partitionID uint32) func() {
	rs.recomputeFloorLocked(s, partitionID)
	floor, ok := s.floors[partitionID]
	cb := rs.OnFloorReleased
	if cb == nil {
		return func() {}
	}
	consumerID := s.consumer.ID()
	return func() { cb(consumerID, partitionID, floor, ok) }
}

// Floor returns the current cursor floor for (consumerID, partitionID): the
// lowest seq of any record still queued (undelivered) in a blocked message
// group for that consumer on that partition. ok is false when there is no
// blocked group on that partition, meaning the caller may persist any seq
// without risking the loss of a queued-but-undelivered follow-on (RTR-06).
func (rs *RetryScheduler) Floor(consumerID string, partitionID uint32) (uint64, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	s, ok := rs.states[consumerID]
	if !ok {
		return 0, false
	}
	seq, ok := s.floors[partitionID]
	return seq, ok
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
// never across Deliver or DLQ Store.Write.
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
			// RTR-06: a head just left the queue, which can only raise the
			// floor (or clear it). Recompute and notify Router so it can
			// re-persist the released cursor even if this partition then
			// goes quiet with no new events to trigger a dispatch.
			notify := rs.afterPopLocked(s, rec.Entry.PartitionID)
			rs.mu.Unlock()
			notify()
		case isPermanentError(err):
			rec.LastErr = err
			rs.mu.Unlock()
			rs.deadLetterHead(ctx, s, groupKey, rec)
		default:
			rec.LastErr = err
			rec.Attempts++
			if rec.Attempts >= maxRetries {
				rs.mu.Unlock()
				rs.deadLetterHead(ctx, s, groupKey, rec)
				continue
			}
			rec.NextRetryAt = time.Now().Add(JitteredDelay(rec.Attempts))
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

// buildDLQEntry constructs a dlq.Entry from a RetryRecord. Payload prefers
// Entry.Raw and falls back to json.Marshal of the ChangeEvent. Reason is
// truncated to maxReasonBytes.
func buildDLQEntry(rec *RetryRecord) dlq.Entry {
	payload := rec.Entry.Raw
	if len(payload) == 0 && rec.Entry.Event != nil {
		payload, _ = json.Marshal(rec.Entry.Event)
	}
	reason := ""
	if rec.LastErr != nil {
		reason = rec.LastErr.Error()
		if len(reason) > maxReasonBytes {
			reason = reason[:maxReasonBytes]
		}
	}
	eventID := ""
	table := ""
	idem := ""
	if rec.Entry.Event != nil {
		eventID = rec.Entry.Event.ID.String()
		table = rec.Entry.Event.Table
		idem = rec.Entry.Event.IdempotencyKey
	}
	return dlq.Entry{
		ConsumerID:     rec.ConsumerID,
		EventID:        eventID,
		Table:          table,
		PartitionID:    rec.Entry.PartitionID,
		Seq:            rec.Entry.Seq,
		Attempts:       rec.Attempts,
		Reason:         reason,
		IdempotencyKey: idem,
		Payload:        payload,
	}
}

// popHeadLocked removes the head of groupKey's queue. If follow-ons remain,
// the next entry becomes head and is made immediately eligible. Caller must
// hold rs.mu and have already verified queue[0] is the record being popped.
func popHeadLocked(s *consumerRetryState, groupKey string, queue []*RetryRecord) {
	if len(queue) == 1 {
		delete(s.blockedGroups, groupKey)
		return
	}
	queue[1].NextRetryAt = time.Now()
	s.blockedGroups[groupKey] = queue[1:]
}

// deadLetterHead persists the head record to the DLQ (when configured) and
// pops it from the blocked queue. Store.Write runs with rs.mu RELEASED —
// the same off-lock discipline drainGroup uses for Deliver.
//
// DLQ-01: if Write fails, the head is NOT popped. NextRetryAt is pushed to
// the plateau so both delivery and the DLQ write are re-attempted on the
// next plateau tick. The RTR-06 floor stays pinned; cursors do not advance
// past the stuck event. This is the intentional no-silent-loss trade-off.
//
// Caller must NOT hold rs.mu.
func (rs *RetryScheduler) deadLetterHead(ctx context.Context, s *consumerRetryState, groupKey string, rec *RetryRecord) {
	entry := buildDLQEntry(rec)

	rs.mu.Lock()
	store := rs.dlq
	metrics := rs.metrics
	rs.mu.Unlock()

	if store == nil {
		// Disabled: today's behavior exactly — log + pop.
		rs.mu.Lock()
		queue := s.blockedGroups[groupKey]
		if len(queue) == 0 || queue[0] != rec {
			rs.mu.Unlock()
			return
		}
		slog.Error("router: dead-letter",
			"consumer_id", rec.ConsumerID,
			"event_id", rec.Entry.Event.ID.String(),
			"table", rec.Entry.Event.Table,
			"partition", rec.Entry.PartitionID,
			"seq", rec.Entry.Seq,
			"attempts", rec.Attempts,
		)
		popHeadLocked(s, groupKey, queue)
		notify := rs.afterPopLocked(s, rec.Entry.PartitionID)
		rs.mu.Unlock()
		notify()
		return
	}

	// Write with lock RELEASED.
	writeErr := store.Write(ctx, entry)

	rs.mu.Lock()
	queue := s.blockedGroups[groupKey]
	if len(queue) == 0 || queue[0] != rec {
		// Head changed while we were writing — bail cleanly (do not pop the
		// new head, do not mutate NextRetryAt on a different record).
		rs.mu.Unlock()
		return
	}

	if writeErr != nil {
		// DLQ-01: do NOT pop. Plateau both delivery and DLQ write retries.
		maxAttempt := len(retryDelays) - 1
		rec.NextRetryAt = time.Now().Add(JitteredDelay(maxAttempt))
		if metrics != nil {
			metrics.DLQWriteFailuresTotal.WithLabelValues(rec.ConsumerID).Inc()
		}
		slog.Error("router: dead-letter DLQ write failed",
			"consumer_id", rec.ConsumerID,
			"event_id", rec.Entry.Event.ID.String(),
			"table", rec.Entry.Event.Table,
			"partition", rec.Entry.PartitionID,
			"seq", rec.Entry.Seq,
			"attempts", rec.Attempts,
			"err", writeErr,
		)
		rs.mu.Unlock()
		return
	}

	slog.Error("router: dead-letter",
		"consumer_id", rec.ConsumerID,
		"event_id", rec.Entry.Event.ID.String(),
		"table", rec.Entry.Event.Table,
		"partition", rec.Entry.PartitionID,
		"seq", rec.Entry.Seq,
		"attempts", rec.Attempts,
		"dlq", "persisted",
	)
	popHeadLocked(s, groupKey, queue)
	if metrics != nil {
		metrics.DLQEventsTotal.WithLabelValues(rec.ConsumerID).Inc()
	}
	notify := rs.afterPopLocked(s, rec.Entry.PartitionID)
	rs.mu.Unlock()
	notify()
}
