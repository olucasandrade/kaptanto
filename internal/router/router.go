// Package router implements the fan-out delivery layer of kaptanto. It reads
// events from the EventLog and delivers them to registered Consumer
// implementations (stdout, SSE, gRPC).
//
// Per-key ordering invariant: events for the same message group key are always
// delivered in order. A failed delivery for key K blocks only subsequent events
// for K; events for other keys continue unaffected (RTR-04).
package router

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// poisonStreakLimit is the number of consecutive PermanentFlushError
// dead-letters on one consumer+partition before further poisons are treated
// as transient (RTR-07 poison-streak guard). Fixed; not configurable.
const poisonStreakLimit = 25

// pollInterval is the fallback timer used when the EventLog does not implement
// PartitionNotifier (e.g. fakes in tests) or when a notify signal is missed
// in the race window between the cursor read and the channel select. It is
// intentionally long because the notify path handles normal low-rate delivery;
// this timer is purely a safety net.
const pollInterval = 500 * time.Millisecond

// Consumer is the output interface that all delivery targets (stdout, SSE,
// gRPC) must implement. Deliver is called sequentially within a message group;
// implementations must be safe for concurrent calls across different groups.
type Consumer interface {
	// ID returns a stable, unique identifier for this consumer instance.
	ID() string

	// Deliver delivers a single event to the consumer. Returning a non-nil error
	// causes the event's message group to be blocked for this consumer until the
	// next restart (RTR-04).
	Deliver(ctx context.Context, entry eventlog.LogEntry) error
}

// BatchFlusher is an optional interface that Consumers may implement to
// coalesce network flushes. If a Consumer implements BatchFlusher, the Router
// calls FlushBatch once after dispatching each ReadPartition batch instead of
// relying on per-event flushes inside Deliver. This amortises flush latency
// (e.g. http.Flusher, a broker producer's batch API) over an entire batch,
// significantly increasing delivery throughput on high-latency transports.
//
// Cursor semantics (CHK-01, queue-sink-flushbatch-loss fix): for a
// BatchFlusher consumer, a successful Deliver only buffers the event in
// memory — it does NOT advance the consumer's durable cursor. Instead the
// Router records a provisional cursor advance. The provisional advance is
// promoted to the durable, persisted cursor only after FlushBatch returns
// nil for that partition. If FlushBatch returns an error, the provisional
// advance for that partition is discarded: the consumer's durable cursor is
// unchanged, so the next ReadPartition call naturally re-reads and
// re-delivers the same window (per-key order is preserved because entries
// are re-delivered in the same seq order). Consecutive FlushBatch failures on
// a partition apply the RetryScheduler's NextDelay backoff schedule before
// the partition is polled again, so a persistently unreachable broker does
// not hot-loop ReadPartition.
//
// PermanentFlushError (RTR-07): when FlushBatch returns a PermanentFlushError
// naming one poisoned seq, the router dead-letters that seq to the DLQ, records
// it in a per-consumer skip-set, discards the provisional advance, and resets
// flusher backoff. Subsequent dispatches skip the poisoned seq; the rest of
// the window is re-flushed. A poison-streak guard (25 consecutive dead-letters
// with no successful flush) treats further poisons as transient to avoid
// draining a misconfigured endpoint into the DLQ.
//
// Re-reading a failed window can re-deliver entries to OTHER consumers
// registered on the same partition; this is harmless because each consumer's
// own cursor already advanced past anything it successfully processed (see
// the entry.Seq < snap.cursor guard in dispatch). It can also cause a
// partial-success batch (broker acked some records, failed others) to be
// re-delivered in full, producing broker-side duplicates for the
// already-acked records — this is acceptable under at-least-once delivery;
// DLV-04's idempotency keys make such duplicates dedupable downstream.
type BatchFlusher interface {
	// FlushBatch flushes any buffered writes to the underlying transport.
	// Called by runPartition after processing each batch of entries. A nil
	// return promotes this consumer's provisional cursor for partitionID to
	// the durable, persisted cursor. A non-nil return discards the
	// provisional advance so the batch is re-read and re-delivered on the
	// next poll, after a NextDelay-scheduled backoff.
	FlushBatch(ctx context.Context, partitionID uint32) error
}

// cursorSave is a deferred SaveCursor call issued after releasing Router.mu.
type cursorSave struct {
	consumerID string
	partition  uint32
	seq        uint64
}

// ConsumerCursorStore persists per-consumer, per-partition delivery cursors so
// consumers resume from the correct position after a restart.
type ConsumerCursorStore interface {
	// SaveCursor persists the last successfully delivered seq for a consumer
	// partition. The implementation must be idempotent and upsert-safe.
	SaveCursor(ctx context.Context, consumerID string, partitionID uint32, seq uint64) error

	// LoadCursor retrieves the last saved seq for a consumer partition.
	// Returns 1 (not 0) when no cursor has been saved — seq 0 is the dedup
	// sentinel and must never be used as a start position (RTR-03).
	LoadCursor(ctx context.Context, consumerID string, partitionID uint32) (uint64, error)
}

// CursorDeleter is an optional interface a ConsumerCursorStore may implement
// to remove all cursors for a consumer. Router.Unregister uses it (when
// available) so ephemeral consumers such as MCP session subscriptions do not
// leak durable cursor rows.
type CursorDeleter interface {
	// DeleteCursor removes every persisted cursor for consumerID. It must
	// tolerate unknown consumer IDs.
	DeleteCursor(ctx context.Context, consumerID string) error
}

// noopCursorStore is an in-memory ConsumerCursorStore with no persistence.
// It is safe only for single-goroutine use per consumer and is used when
// NewRouter receives a nil cursorStore argument.
type noopCursorStore struct {
	mu   sync.Mutex
	data map[string]uint64
}

// NewNoopCursorStore returns a new in-memory ConsumerCursorStore.
// LoadCursor returns 1 for keys not yet written.
func NewNoopCursorStore() ConsumerCursorStore {
	return &noopCursorStore{data: make(map[string]uint64)}
}

func noopKey(consumerID string, partitionID uint32) string {
	return consumerID + ":" + strconv.FormatUint(uint64(partitionID), 10)
}

func (n *noopCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	n.mu.Lock()
	n.data[noopKey(consumerID, partitionID)] = seq
	n.mu.Unlock()
	return nil
}

func (n *noopCursorStore) LoadCursor(_ context.Context, consumerID string, partitionID uint32) (uint64, error) {
	n.mu.Lock()
	v, ok := n.data[noopKey(consumerID, partitionID)]
	n.mu.Unlock()
	if !ok {
		return 1, nil
	}
	return v, nil
}

// DeleteCursor implements CursorDeleter for the in-memory store.
func (n *noopCursorStore) DeleteCursor(_ context.Context, consumerID string) error {
	prefix := consumerID + ":"
	n.mu.Lock()
	for k := range n.data {
		if strings.HasPrefix(k, prefix) {
			delete(n.data, k)
		}
	}
	n.mu.Unlock()
	return nil
}

// consumerState tracks per-consumer runtime state: the last successfully
// delivered seq per partition. Blocked message group state is owned by
// RetryScheduler (rs field on Router), not by consumerState.
type consumerState struct {
	consumer          Consumer
	cursorByPartition map[uint32]uint64

	// removed marks a soft-unregistered consumer. Indices stay stable so
	// in-flight dispatch phases remain valid; dispatch skips removed slots.
	removed bool

	// isBatchFlusher and provisionalByPartition implement the
	// queue-sink-flushbatch-loss fix: for consumers that implement
	// BatchFlusher, a successful Deliver during dispatch does not touch
	// cursorByPartition directly. Instead it records a provisional advance
	// here. runPartition promotes provisionalByPartition[p] into
	// cursorByPartition[p] (and persists it) only after FlushBatch(p)
	// returns nil; on failure the provisional advance is discarded so the
	// next ReadPartition naturally re-reads the same window. Both fields are
	// only populated/consulted when isBatchFlusher is true.
	isBatchFlusher         bool
	provisionalByPartition map[uint32]uint64

	// skippedSeqs is the RTR-07 poison skip-set: seqs that were permanently
	// flush-failed, durably written to the DLQ, and must not be re-delivered
	// to this consumer. Lazy (nil until the first poison skip). Memory-only —
	// after a crash the poison re-delivers, re-poisons, and DLQ-02 dedup
	// re-skips. Pruned when promoteProvisional advances the durable cursor
	// past a skipped seq.
	skippedSeqs map[uint32]map[uint64]struct{}

	// poisonStreak counts consecutive PermanentFlushError dead-letters per
	// partition with no intervening successful flush. poisonStreakLogged
	// records that the misconfiguration warning was emitted once for that
	// partition. Both reset on successful FlushBatch.
	poisonStreak       map[uint32]int
	poisonStreakLogged map[uint32]struct{}

	// poisonNoDLQLogged tracks seqs for which we already logged that DLQ is
	// disabled (one loud log per seq, forever-transient path).
	poisonNoDLQLogged map[uint32]map[uint64]struct{}
}

// consumerSnap is a lightweight snapshot of a consumer's state captured at
// the start of dispatch. It is reused across events via per-partition scratch
// buffers to avoid per-event heap allocations.
type consumerSnap struct {
	consumer Consumer
	blocked  bool
	cursor   uint64 // this consumer's next-seq for the partition at snapshot time
	skipped  bool   // entry.Seq is in this consumer's RTR-07 skip-set
}

// Router reads from the EventLog and delivers events to all registered
// Consumers. One goroutine per partition is used; goroutines run for the
// lifetime of the context passed to Run.
type Router struct {
	eventLog        eventlog.EventLog
	numPartitions   uint32
	consumers       []consumerState
	mu              sync.RWMutex
	cursorStore     ConsumerCursorStore
	rs              *RetryScheduler
	metrics         *observability.KaptantoMetrics
	dlq             dlq.Store         // optional; nil disables FlushBatch poison skip (DLQ-01)
	ownedPartitions []uint32          // nil = all partitions (non-cluster default)
	notifyChs       []<-chan struct{} // per-partition notify channels; nil if EventLog doesn't support it
}

// NewRouter creates a new Router. If cs is nil, an in-memory noopCursorStore
// is used (delivery positions are not persisted across restarts).
//
// If el implements eventlog.PartitionNotifier, NewRouter wires the per-partition
// notify channels so runPartition blocks on new-event signals instead of
// spinning on the fallback timer. EventLogs that do not implement
// PartitionNotifier (e.g. fakes in tests) fall back to pure timer polling.
func NewRouter(el eventlog.EventLog, numPartitions uint32, cs ConsumerCursorStore) *Router {
	if cs == nil {
		cs = NewNoopCursorStore()
	}
	r := &Router{
		eventLog:      el,
		numPartitions: numPartitions,
		cursorStore:   cs,
		rs:            NewRetryScheduler(),
	}
	// RTR-06: when a blocked group's head is popped (delivered or
	// dead-lettered), the RetryScheduler's floor for that partition may rise
	// or clear. Re-persist the released cursor immediately so a partition
	// that goes quiet afterwards doesn't leave the cursor store pinned at a
	// stale floor until the next unrelated event happens to be dispatched.
	r.rs.OnFloorReleased = r.handleFloorRelease
	if pn, ok := el.(eventlog.PartitionNotifier); ok {
		r.notifyChs = make([]<-chan struct{}, numPartitions)
		for i := uint32(0); i < numPartitions; i++ {
			r.notifyChs[i] = pn.NotifyCh(i)
		}
	}
	return r
}

// SetMetrics injects a KaptantoMetrics reference for ConsumerLag reporting.
// Call after construction, before Run.
func (r *Router) SetMetrics(m *observability.KaptantoMetrics) {
	r.metrics = m
}

// SetDLQ wires a dead-letter store for the FlushBatch poison path (RTR-07).
// A nil store disables poison skip: PermanentFlushError is treated as
// transient forever (DLQ-01 — never skip without a durable copy). Call after
// construction, before Run.
func (r *Router) SetDLQ(store dlq.Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dlq = store
}

// RetryScheduler returns the Router's internal retry scheduler. Exposed so
// production wiring can inject DLQ and metrics stores before Run.
func (r *Router) RetryScheduler() *RetryScheduler {
	return r.rs
}

// SetOwnedPartitions configures which partitions this Router instance reads.
// Must be called before Run. Passing nil (default) restores "all partitions"
// behavior — non-cluster mode is byte-for-byte identical to pre-Phase-16.
func (r *Router) SetOwnedPartitions(owned []uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ownedPartitions = owned
}

// allPartitions returns a slice [0, 1, ..., n-1].
func allPartitions(n uint32) []uint32 {
	s := make([]uint32, n)
	for i := uint32(0); i < n; i++ {
		s[i] = i
	}
	return s
}

// Register adds a Consumer to the Router. Safe to call while Run is active
// (e.g. SSE / MCP session-scoped consumers). The initial delivery cursor for
// each partition is loaded from the ConsumerCursorStore.
func (r *Router) Register(c Consumer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, isBatchFlusher := c.(BatchFlusher)
	cs := consumerState{
		consumer:          c,
		cursorByPartition: make(map[uint32]uint64),
		isBatchFlusher:    isBatchFlusher,
	}
	if isBatchFlusher {
		cs.provisionalByPartition = make(map[uint32]uint64)
	}

	ctx := context.Background()
	for p := uint32(0); p < r.numPartitions; p++ {
		seq, err := r.cursorStore.LoadCursor(ctx, c.ID(), p)
		if err != nil {
			slog.Warn("router: failed to load cursor", "consumer", c.ID(), "partition", p, "err", err)
			seq = 1
		}
		cs.cursorByPartition[p] = seq
	}

	r.consumers = append(r.consumers, cs)
}

// Unregister soft-removes the consumer with the given ID. Indices stay stable
// so in-flight dispatch phases remain valid. Returns false when no active
// consumer matched. Used by MCP session cleanup (MCP-02). When the cursor
// store implements CursorDeleter, the consumer's durable cursors are deleted
// too — ephemeral consumers (MCP session subscriptions) never resume, so
// keeping their rows would grow the cursor store without bound.
func (r *Router) Unregister(id string) bool {
	r.mu.Lock()
	found := false
	for i := range r.consumers {
		cs := &r.consumers[i]
		if cs.removed || cs.consumer == nil {
			continue
		}
		if cs.consumer.ID() == id {
			cs.removed = true
			cs.consumer = nil
			cs.provisionalByPartition = nil
			cs.skippedSeqs = nil
			found = true
			break
		}
	}
	r.mu.Unlock()
	if !found {
		return false
	}
	if d, ok := r.cursorStore.(CursorDeleter); ok {
		if err := d.DeleteCursor(context.Background(), id); err != nil {
			slog.Warn("router: delete cursors on unregister", "consumer", id, "err", err)
		}
	}
	return true
}

// ConsumerCount returns the number of active (non-unregistered) consumers.
func (r *Router) ConsumerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, cs := range r.consumers {
		if !cs.removed && cs.consumer != nil {
			n++
		}
	}
	return n
}

// Run starts one goroutine per owned partition and blocks until ctx is
// cancelled. When ownedPartitions is nil (default), all numPartitions
// partitions are started — behavior is identical to pre-Phase-16.
// Returns nil when ctx.Done() fires — it never returns a non-nil error for
// transient ReadPartition failures.
func (r *Router) Run(ctx context.Context) error {
	go r.rs.Run(ctx)

	r.mu.RLock()
	partitions := r.ownedPartitions
	r.mu.RUnlock()
	if partitions == nil {
		partitions = allPartitions(r.numPartitions)
	}

	var wg sync.WaitGroup
	for _, p := range partitions {
		wg.Add(1)
		go func(partitionID uint32) {
			defer wg.Done()
			r.runPartition(ctx, partitionID)
		}(p)
	}
	wg.Wait()
	return nil
}

// flusherBackoff tracks consecutive FlushBatch failures for one BatchFlusher
// consumer on one partition, so runPartition can apply the RetryScheduler's
// NextDelay backoff schedule instead of hot-looping ReadPartition against a
// consumer whose broker is unreachable (queue-sink-flushbatch-loss fix,
// plan step 4).
type flusherBackoff struct {
	attempts int
	nextAt   time.Time
}

// runPartition is the per-partition poll loop. It reads events sequentially
// and dispatches each to all registered consumers. On empty batch (or error)
// it waits for a notify signal from the EventLog writer or a fallback timer
// before retrying. The fallback timer (pollInterval) acts as a safety net for
// missed signals; the notify path delivers sub-millisecond wakeup on write.
//
// Each goroutine owns its own snaps and deliveryErrs scratch slices that are
// grown as needed and reset ([:0]) per event. This eliminates the two
// per-event heap allocations that the previous dispatch signature caused.
func (r *Router) runPartition(ctx context.Context, partitionID uint32) {
	if rel, ok := r.eventLog.(eventlog.PartitionReleaser); ok {
		defer rel.ReleasePartition(partitionID)
	}

	// Capture the notify channel once; nil if EventLog doesn't implement
	// PartitionNotifier (fakes/tests fall back to pure timer polling).
	var notifyCh <-chan struct{}
	if r.notifyChs != nil {
		notifyCh = r.notifyChs[partitionID]
	}

	// waitForWork blocks until a notify signal fires, the fallback timer fires,
	// or ctx is cancelled. Returns false if ctx is done.
	waitForWork := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-notifyCh: // nil channel blocks forever — fallback to timer only
			return true
		case <-time.After(pollInterval):
			return true
		}
	}

	// Per-partition scratch buffers — owned exclusively by this goroutine.
	// Grown on demand; reset to [:0] before each dispatch call.
	var snaps []consumerSnap
	var deliveryErrs []error

	// backoffState tracks consecutive FlushBatch failures per consumer index
	// on this partition (queue-sink-flushbatch-loss fix), keyed by fe.idx
	// rather than the BatchFlusher value itself — the interface doesn't
	// require comparability, so a non-comparable concrete flusher type would
	// panic on map access. fe.idx is stable because the router never
	// unregisters consumers. Scoped to this goroutine — safe without locking
	// since only this partition's goroutine ever touches it.
	backoffState := make(map[int]*flusherBackoff)
	// backoffUntil is the latest nextAt across all consumers currently in
	// backoff; the loop waits until this time before the next ReadPartition
	// attempt, so a down broker does not cause a tight re-read/re-flush loop.
	var backoffUntil time.Time

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !backoffUntil.IsZero() {
			if remaining := time.Until(backoffUntil); remaining > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(remaining):
				}
			}
			backoffUntil = time.Time{}
		}

		nextSeq := r.minCursorForPartition(partitionID)

		entries, err := r.eventLog.ReadPartition(ctx, partitionID, nextSeq, 256)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("router: ReadPartition error", "partition", partitionID, "err", err)
			if !waitForWork() {
				return
			}
			continue
		}

		if len(entries) == 0 {
			if !waitForWork() {
				return
			}
			continue
		}

		for _, entry := range entries {
			select {
			case <-ctx.Done():
				return
			default:
			}
			r.dispatch(ctx, partitionID, entry, &snaps, &deliveryErrs)
		}

		// Flush once per batch for consumers that implement BatchFlusher, then
		// promote or discard each consumer's provisional cursor advance based
		// on the flush outcome (queue-sink-flushbatch-loss fix).
		backoffUntil = r.flushBatchConsumers(ctx, partitionID, backoffState, backoffUntil)
	}
}

// flusherEntry pairs a BatchFlusher consumer with its index into r.consumers,
// which promoteProvisional/discardProvisional need to locate the consumer's
// provisional cursor state.
type flusherEntry struct {
	bf  BatchFlusher
	idx int
}

// flushBatchConsumers flushes every BatchFlusher consumer once for
// partitionID, then promotes or discards that consumer's provisional cursor
// advance based on the flush outcome (queue-sink-flushbatch-loss fix). A
// failed flush discards the provisional advance — so the next ReadPartition
// re-reads and re-delivers the same window — and extends backoffState's
// NextDelay schedule for that consumer so a persistently unreachable broker
// does not cause a hot loop. Returns the backoffUntil the caller should wait
// on before its next ReadPartition attempt (unchanged if no flush failed).
//
// Acquiring RLock to snapshot the consumer list is safe here; the flush
// itself happens outside the lock. If ctx is cancelled mid-loop, remaining
// flushers are skipped — runPartition's outer loop checks ctx.Done() again
// on its next iteration and returns there.
func (r *Router) flushBatchConsumers(ctx context.Context, partitionID uint32, backoffState map[int]*flusherBackoff, backoffUntil time.Time) time.Time {
	r.mu.RLock()
	flushers := make([]flusherEntry, 0, len(r.consumers))
	for i, cs := range r.consumers {
		if cs.removed || cs.consumer == nil {
			continue
		}
		if bf, ok := cs.consumer.(BatchFlusher); ok {
			flushers = append(flushers, flusherEntry{bf: bf, idx: i})
		}
	}
	r.mu.RUnlock()

	for _, fe := range flushers {
		if ctx.Err() != nil {
			return backoffUntil
		}
		if err := fe.bf.FlushBatch(ctx, partitionID); err != nil {
			var pfe *PermanentFlushError
			if errors.As(err, &pfe) {
				if r.handlePoisonFlush(ctx, fe.idx, partitionID, pfe, backoffState) {
					continue
				}
			}
			slog.Warn("router: batch flush error", "partition", partitionID, "err", err)
			r.discardProvisional(fe.idx, partitionID)
			bo := backoffState[fe.idx]
			if bo == nil {
				bo = &flusherBackoff{}
				backoffState[fe.idx] = bo
			}
			bo.attempts++
			bo.nextAt = time.Now().Add(JitteredDelay(bo.attempts - 1))
			if backoffUntil.IsZero() || bo.nextAt.After(backoffUntil) {
				backoffUntil = bo.nextAt
			}
			continue
		}
		// Flush succeeded — clear any prior backoff, reset poison streak,
		// and promote the provisional cursor to the durable, persisted cursor.
		delete(backoffState, fe.idx)
		r.resetPoisonStreak(fe.idx, partitionID)
		r.promoteProvisional(ctx, fe.idx, partitionID)
	}
	return backoffUntil
}

// handlePoisonFlush implements RTR-07 for a PermanentFlushError from FlushBatch.
// On success it dead-letters the poisoned seq, records it in the skip-set,
// discards the provisional cursor, and resets flusher backoff. Returns true
// when the poison was durably skipped; false means fall through to transient
// handling (never skip without a durable DLQ copy — DLQ-01).
//
// Ordering (hard rule): dlq.Write must return nil before the seq is added to
// the skip-set; then discard provisional. Never extend backoff after a
// successful poison skip.
func (r *Router) handlePoisonFlush(ctx context.Context, idx int, partitionID uint32, pfe *PermanentFlushError, backoffState map[int]*flusherBackoff) bool {
	r.mu.Lock()
	if idx >= len(r.consumers) {
		r.mu.Unlock()
		return false
	}
	cs := &r.consumers[idx]
	if cs.removed || cs.consumer == nil {
		r.mu.Unlock()
		return false
	}
	streak := 0
	if cs.poisonStreak != nil {
		streak = cs.poisonStreak[partitionID]
	}
	if streak >= poisonStreakLimit {
		if cs.poisonStreakLogged == nil {
			cs.poisonStreakLogged = make(map[uint32]struct{})
		}
		if _, logged := cs.poisonStreakLogged[partitionID]; !logged {
			cs.poisonStreakLogged[partitionID] = struct{}{}
			slog.Error("router: poison streak — suspected endpoint misconfiguration",
				"consumer", cs.consumer.ID(),
				"partition", partitionID,
				"streak", streak,
			)
		}
		r.mu.Unlock()
		return false
	}
	cursor := cs.cursorByPartition[partitionID]
	consumerID := cs.consumer.ID()
	store := r.dlq
	metrics := r.metrics
	r.mu.Unlock()

	fromSeq := cursor
	if fromSeq == 0 {
		fromSeq = 1
	}
	// cursorByPartition stores next-seq (last-delivered+1), the ReadPartition fromSeq.
	entries, err := r.eventLog.ReadPartition(ctx, partitionID, fromSeq, 256)
	if err != nil {
		slog.Error("router: poison flush re-read failed",
			"consumer", consumerID,
			"partition", partitionID,
			"seq", pfe.Seq,
			"err", err,
		)
		return false
	}
	var found *eventlog.LogEntry
	for i := range entries {
		if entries[i].Seq == pfe.Seq {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		slog.Error("router: poison flush seq not found in window",
			"consumer", consumerID,
			"partition", partitionID,
			"seq", pfe.Seq,
			"from_seq", fromSeq,
		)
		return false
	}

	if store == nil {
		r.logPoisonDLQDisabledOnce(idx, partitionID, pfe.Seq, consumerID)
		return false
	}

	found.PartitionID = partitionID
	dlqEntry := buildDLQEntry(&RetryRecord{
		Entry:      *found,
		Attempts:   1,
		ConsumerID: consumerID,
		LastErr:    pfe,
	})
	if writeErr := store.Write(ctx, dlqEntry); writeErr != nil {
		if metrics != nil {
			metrics.DLQWriteFailuresTotal.WithLabelValues(consumerID).Inc()
		}
		slog.Error("router: poison flush DLQ write failed",
			"consumer", consumerID,
			"partition", partitionID,
			"seq", pfe.Seq,
			"err", writeErr,
		)
		return false
	}
	if metrics != nil {
		metrics.DLQEventsTotal.WithLabelValues(consumerID).Inc()
	}
	slog.Error("router: poison flush dead-lettered",
		"consumer", consumerID,
		"partition", partitionID,
		"seq", pfe.Seq,
		"dlq", "persisted",
	)

	// Durable write succeeded — now record skip, discard provisional, reset backoff.
	r.mu.Lock()
	if idx < len(r.consumers) {
		cs := &r.consumers[idx]
		if cs.skippedSeqs == nil {
			cs.skippedSeqs = make(map[uint32]map[uint64]struct{})
		}
		if cs.skippedSeqs[partitionID] == nil {
			cs.skippedSeqs[partitionID] = make(map[uint64]struct{})
		}
		cs.skippedSeqs[partitionID][pfe.Seq] = struct{}{}
		if cs.poisonStreak == nil {
			cs.poisonStreak = make(map[uint32]int)
		}
		cs.poisonStreak[partitionID]++
	}
	r.mu.Unlock()

	r.discardProvisional(idx, partitionID)
	delete(backoffState, idx)
	return true
}

// logPoisonDLQDisabledOnce emits one loud error per (consumer, partition, seq)
// when DLQ is disabled and a PermanentFlushError cannot be skipped.
func (r *Router) logPoisonDLQDisabledOnce(idx int, partitionID uint32, seq uint64, consumerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx >= len(r.consumers) {
		return
	}
	cs := &r.consumers[idx]
	if cs.poisonNoDLQLogged == nil {
		cs.poisonNoDLQLogged = make(map[uint32]map[uint64]struct{})
	}
	if cs.poisonNoDLQLogged[partitionID] == nil {
		cs.poisonNoDLQLogged[partitionID] = make(map[uint64]struct{})
	}
	if _, ok := cs.poisonNoDLQLogged[partitionID][seq]; ok {
		return
	}
	cs.poisonNoDLQLogged[partitionID][seq] = struct{}{}
	slog.Error("router: poison flush with DLQ disabled — treating as transient",
		"consumer", consumerID,
		"partition", partitionID,
		"seq", seq,
	)
}

// resetPoisonStreak clears the consecutive-poison counter for a consumer
// partition after a successful FlushBatch.
func (r *Router) resetPoisonStreak(idx int, partitionID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx >= len(r.consumers) {
		return
	}
	cs := &r.consumers[idx]
	if cs.poisonStreak != nil {
		delete(cs.poisonStreak, partitionID)
	}
	if cs.poisonStreakLogged != nil {
		delete(cs.poisonStreakLogged, partitionID)
	}
}

// promoteProvisional promotes consumer index idx's provisional cursor advance
// for partitionID into its durable cursor and persists it via cursorStore.
// Called by runPartition after FlushBatch returns nil for a BatchFlusher
// consumer (queue-sink-flushbatch-loss fix). No-op if idx is out of range or
// the consumer was Unregister'd, or there is no pending provisional advance.
func (r *Router) promoteProvisional(ctx context.Context, idx int, partitionID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx >= len(r.consumers) {
		return
	}
	cs := &r.consumers[idx]
	if cs.removed || cs.consumer == nil || cs.provisionalByPartition == nil {
		return
	}
	provisional, ok := cs.provisionalByPartition[partitionID]
	if !ok {
		return
	}
	delete(cs.provisionalByPartition, partitionID)
	if provisional > cs.cursorByPartition[partitionID] {
		cs.cursorByPartition[partitionID] = provisional
	}
	newCursor := cs.cursorByPartition[partitionID]
	// RTR-07: prune skip-set entries at or below the newly persisted cursor.
	if m := cs.skippedSeqs[partitionID]; m != nil {
		for seq := range m {
			if seq <= newCursor {
				delete(m, seq)
			}
		}
		if len(m) == 0 {
			delete(cs.skippedSeqs, partitionID)
		}
	}
	persistSeq := newCursor
	if floor, ok := r.rs.Floor(cs.consumer.ID(), partitionID); ok && floor < persistSeq {
		persistSeq = floor
	}
	if err := r.cursorStore.SaveCursor(ctx, cs.consumer.ID(), partitionID, persistSeq); err != nil {
		slog.Warn("router: failed to save cursor after batch flush",
			"consumer", cs.consumer.ID(),
			"partition", partitionID,
			"seq", persistSeq,
			"err", err,
		)
	}
	if r.metrics != nil {
		r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Set(0)
	}
}

// discardProvisional discards consumer index idx's pending provisional cursor
// advance for partitionID after a failed FlushBatch, so the next
// ReadPartition call naturally re-reads and re-delivers the same window
// (queue-sink-flushbatch-loss fix). No-op if idx is out of range or the
// consumer was Unregister'd.
func (r *Router) discardProvisional(idx int, partitionID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx >= len(r.consumers) {
		return
	}
	cs := &r.consumers[idx]
	if cs.removed || cs.consumer == nil {
		return
	}
	if cs.provisionalByPartition != nil {
		delete(cs.provisionalByPartition, partitionID)
	}
	if r.metrics != nil {
		r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Add(1)
	}
}

// minCursorForPartition returns the minimum cursor across all consumers for
// the given partition. This determines the fromSeq for ReadPartition so no
// consumer misses an event. Returns 1 when there are no consumers.
func (r *Router) minCursorForPartition(partitionID uint32) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	min := uint64(0)
	for _, cs := range r.consumers {
		if cs.removed || cs.consumer == nil {
			continue
		}
		cur := cs.cursorByPartition[partitionID]
		if min == 0 || cur < min {
			min = cur
		}
	}
	if min == 0 {
		return 1
	}
	return min
}

// dispatch delivers entry to every registered consumer, respecting per-key
// blocking. If a consumer has a blocked group for entry's key, that consumer
// skips the entry. On delivery error the entry's key is added to blockedGroups
// for that consumer. On success the consumer's cursor is advanced and saved.
//
// Fix C: Deliver is called outside r.mu to decouple SSE I/O (JSON encode +
// HTTP write + Flush) from the partition read loop. This prevents all 64
// partition goroutines from serialising through one synchronous network write.
// The lock is held only for the consumer snapshot (RLock) and cursor updates
// (Lock), both of which are fast in-memory operations.
//
// snapsPtr and errsPtr are per-partition scratch buffers owned by the calling
// runPartition goroutine. They are grown on demand and reset ([:0]) at the
// start of each call, eliminating two per-event heap allocations on the hot path.
func (r *Router) dispatch(ctx context.Context, partitionID uint32, entry eventlog.LogEntry, snapsPtr *[]consumerSnap, errsPtr *[]error) {
	// Compute the groupKey lazily: only allocate the string if at least one
	// consumer has a blocked message group. In the common steady-state (no
	// failures) this avoids a string alloc and all per-consumer IsBlocked calls.
	var groupKey string
	hasBlocked := r.rs.HasBlocked()
	if hasBlocked {
		gk, gkErr := entry.GroupingKey()
		if gkErr != nil {
			slog.Warn("router: dispatch grouping key", "partition", partitionID, "seq", entry.Seq, "err", gkErr)
			return
		}
		groupKey = gk
	}

	snaps := r.snapshotConsumers(partitionID, entry.Seq, groupKey, hasBlocked, snapsPtr)
	n := len(snaps)

	// Phase 2: deliver outside the lock. Concurrent partitions can deliver
	// independently; SSE flush latency no longer serialises all goroutines.
	//
	// ReadPartition fetches from the minimum cursor across all consumers, so a
	// lagging or blocked consumer can rewind the read window below an entry that
	// a healthy consumer has already acked. Skip delivery to any consumer whose
	// own cursor is already past entry.Seq — otherwise an unrelated slow consumer
	// causes repeated duplicate delivery to every other consumer in the partition.
	errs := (*errsPtr)[:0]
	if cap(errs) < n {
		errs = make([]error, n)
	} else {
		errs = errs[:n]
		for i := range errs {
			errs[i] = nil
		}
	}
	for i, snap := range snaps {
		if snap.consumer == nil || snap.blocked || ctx.Err() != nil {
			continue
		}
		if entry.Seq < snap.cursor {
			continue // already delivered and acked by this consumer
		}
		if snap.skipped {
			// RTR-07: poison already DLQ'd — do not re-deliver. errs[i]
			// stays nil so Phase 3 advances the (provisional) cursor past it.
			continue
		}
		errs[i] = snap.consumer.Deliver(ctx, entry)
	}
	*errsPtr = errs

	if ctx.Err() != nil {
		return
	}

	// Materialise groupKey if it was skipped above but a delivery just failed —
	// we need it for the slog warning and AddBlocked call in Phase 3.
	if !hasBlocked {
		for _, err := range errs {
			if err != nil {
				gk, gkErr := entry.GroupingKey()
				if gkErr != nil {
					slog.Warn("router: dispatch grouping key", "partition", partitionID, "seq", entry.Seq, "err", gkErr)
					return
				}
				groupKey = gk
				break
			}
		}
	}

	r.applyDispatchCursors(ctx, partitionID, entry, groupKey, snaps, errs)
}

// snapshotConsumers is Phase 1 of dispatch: copy consumer state under RLock.
// Soft-Unregister leaves tombstones so indices remain valid for Phase 3.
func (r *Router) snapshotConsumers(partitionID uint32, seq uint64, groupKey string, hasBlocked bool, snapsPtr *[]consumerSnap) []consumerSnap {
	r.mu.RLock()
	n := len(r.consumers)
	snaps := (*snapsPtr)[:0]
	if cap(snaps) < n {
		snaps = make([]consumerSnap, n)
	} else {
		snaps = snaps[:n]
	}
	for i, cs := range r.consumers {
		if cs.removed || cs.consumer == nil {
			snaps[i] = consumerSnap{}
			continue
		}
		blocked := false
		if hasBlocked {
			blocked = r.rs.IsBlocked(cs.consumer.ID(), groupKey)
		}
		skipped := false
		if m := cs.skippedSeqs[partitionID]; m != nil {
			_, skipped = m[seq]
		}
		snaps[i] = consumerSnap{
			consumer: cs.consumer,
			blocked:  blocked,
			cursor:   cs.cursorByPartition[partitionID],
			skipped:  skipped,
		}
	}
	r.mu.RUnlock()
	*snapsPtr = snaps
	return snaps
}

// applyDispatchCursors is Phase 3 of dispatch: update in-memory cursors under
// write lock, then persist capped cursors after releasing the lock.
func (r *Router) applyDispatchCursors(ctx context.Context, partitionID uint32, entry eventlog.LogEntry, groupKey string, snaps []consumerSnap, errs []error) {
	var saves []cursorSave
	r.mu.Lock()
	for i, snap := range snaps {
		if snap.consumer == nil {
			continue
		}
		if i >= len(r.consumers) {
			break
		}
		cs := &r.consumers[i]
		if cs.removed || cs.consumer == nil || cs.consumer.ID() != snap.consumer.ID() {
			continue
		}
		if snap.skipped {
			r.advanceCursorLocked(cs, partitionID, entry.Seq+1, &saves)
			continue
		}
		r.dispatchUpdateCursor(cs, snap, entry, partitionID, groupKey, errs, i, &saves)
	}
	r.mu.Unlock()
	for _, save := range saves {
		r.saveCursor(ctx, save.consumerID, save.partition, save.seq)
	}
}

// advanceCursorLocked advances cs's in-memory cursor to at least seq and queues
// a capped SaveCursor after Router.mu is released. Must be called under r.mu.
func (r *Router) advanceCursorLocked(cs *consumerState, partitionID uint32, seq uint64, saves *[]cursorSave) {
	if seq > cs.cursorByPartition[partitionID] {
		cs.cursorByPartition[partitionID] = seq
	}
	r.queuePersistCursor(cs, partitionID, cs.cursorByPartition[partitionID], saves)
}

// dispatchUpdateCursor handles Phase 3 per-consumer cursor logic.
// Must be called under r.mu write lock.
func (r *Router) dispatchUpdateCursor(
	cs *consumerState,
	snap consumerSnap,
	entry eventlog.LogEntry,
	partitionID uint32,
	groupKey string,
	errs []error,
	i int,
	saves *[]cursorSave,
) {
	// Normalize entry.PartitionID to the authoritative partitionID this
	// dispatch call was invoked with. RetryScheduler's RTR-06 floor tracking
	// keys off Entry.PartitionID (see AddBlocked/recomputeFloorLocked); relying
	// on the EventLog implementation to always stamp it correctly on every
	// LogEntry it returns is fragile — the production Badger EventLog does,
	// but this keeps the invariant true unconditionally at the point it
	// matters, regardless of the EventLog implementation.
	entry.PartitionID = partitionID
	if snap.blocked {
		if r.metrics != nil {
			r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Add(1)
		}
		if entry.Seq < snap.cursor {
			return
		}
		rec := &RetryRecord{
			Entry:       entry,
			Attempts:    0,
			NextRetryAt: time.Now().Add(NextDelay(0)),
			ConsumerID:  cs.consumer.ID(),
		}
		// AddBlocked queues this follow-on in RetryScheduler and, if it's the
		// first record for this group, lowers the RTR-06 floor for this
		// partition to entry.Seq. persistCursor (below) reads that floor to
		// cap what actually reaches the cursor store.
		r.rs.AddBlocked(cs.consumer, groupKey, rec)
		// The in-memory cursor still advances past the blocked group so
		// ReadPartition's window (minCursorForPartition) and future dispatch
		// calls treat this entry as "seen" for this consumer — required for
		// the follow-on flow (skip re-delivery attempts to the blocked
		// group's own head while it's queued for retry). Only the persisted
		// value is capped.
		nextForFollowOn := entry.Seq + 1
		if nextForFollowOn > cs.cursorByPartition[partitionID] {
			cs.cursorByPartition[partitionID] = nextForFollowOn
		}
		r.queuePersistCursor(cs, partitionID, nextForFollowOn, saves)
		return
	}

	if entry.Seq < snap.cursor {
		return
	}

	if err := errs[i]; err != nil {
		// Identify the event by ULID + partition/seq — never the raw PK
		// (groupKey) or the idempotency key, which embeds the PK. Natural-key
		// PKs must not leak into log pipelines.
		eventID := ""
		table := ""
		if matErr := entry.MaterializeEvent(); matErr == nil && entry.Event != nil {
			eventID = entry.Event.ID.String()
			table = entry.Event.Table
		}
		slog.Warn("router: delivery failed, blocking message group",
			"consumer", cs.consumer.ID(),
			"event_id", eventID,
			"table", table,
			"partition", partitionID,
			"seq", entry.Seq,
			"err", err,
		)
		rec := &RetryRecord{
			Entry:       entry,
			Attempts:    1,
			NextRetryAt: time.Now(),
			ConsumerID:  cs.consumer.ID(),
		}
		r.rs.AddBlocked(cs.consumer, groupKey, rec)
		return
	}

	// RTR-06: this delivery succeeded, but a DIFFERENT key on this same
	// partition may still have a blocked group with a lower seq sitting only
	// in RetryScheduler's memory-only queue. persistCursor caps nextForPartition
	// at that group's floor so a crash right after this SaveCursor can't skip
	// past it — the in-memory cursor still advances, so this consumer isn't
	// redelivered this entry while the process stays up.
	nextForPartition := entry.Seq + 1

	// queue-sink-flushbatch-loss fix: a BatchFlusher consumer's Deliver only
	// buffered the event in memory — no network I/O happened yet. Advancing
	// and persisting the durable cursor here would let the cursor outrun the
	// actual send, so record a provisional advance instead. runPartition
	// promotes it to cs.cursorByPartition (and persists it) only after
	// FlushBatch confirms the buffered batch was actually sent; a flush
	// failure discards the provisional advance so the batch is re-read and
	// re-delivered. This branch is intentionally isolated from the
	// non-batching path below and from the blocked-group follow-on path
	// above (owned by a separate, concurrently developed fix) to keep the
	// change additive.
	if cs.isBatchFlusher {
		if nextForPartition > cs.provisionalByPartition[partitionID] {
			cs.provisionalByPartition[partitionID] = nextForPartition
		}
		return
	}

	if nextForPartition > cs.cursorByPartition[partitionID] {
		cs.cursorByPartition[partitionID] = nextForPartition
	}
	r.queuePersistCursor(cs, partitionID, nextForPartition, saves)
	if r.metrics != nil {
		r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Set(0)
	}
}

// queuePersistCursor records a capped SaveCursor to run after Router.mu unlock.
// Must be called under r.mu write lock.
func (r *Router) queuePersistCursor(cs *consumerState, partitionID uint32, seq uint64, saves *[]cursorSave) {
	persistSeq := seq
	if floor, ok := r.rs.Floor(cs.consumer.ID(), partitionID); ok && floor < persistSeq {
		persistSeq = floor
	}
	*saves = append(*saves, cursorSave{
		consumerID: cs.consumer.ID(),
		partition:  partitionID,
		seq:        persistSeq,
	})
}

// saveCursor persists seq for (consumerID, partitionID). Called outside Router.mu.
func (r *Router) saveCursor(ctx context.Context, consumerID string, partitionID uint32, seq uint64) {
	if err := r.cursorStore.SaveCursor(ctx, consumerID, partitionID, seq); err != nil {
		slog.Warn("router: failed to save cursor",
			"consumer", consumerID,
			"partition", partitionID,
			"seq", seq,
			"err", err,
		)
	}
}

// handleFloorRelease is registered as r.rs.OnFloorReleased. It re-persists
// the cursor for (consumerID, partitionID) once RetryScheduler's floor for it
// rises (a blocked group's head was delivered or dead-lettered) or clears
// entirely (ok == false, no blocked group remains on this partition).
//
// Without this, a partition that goes quiet right after a blocked group
// drains would leave the persisted cursor pinned at the old, now-stale floor
// until the next unrelated event happens to be dispatched on that partition —
// which may be a long time, or never, on a low-traffic table.
//
// Called by RetryScheduler with its own mutex released (see
// RetryScheduler.afterPopLocked), so acquiring r.mu here is safe and follows
// the same Router.mu-then-RetryScheduler.mu order used everywhere else.
func (r *Router) handleFloorRelease(consumerID string, partitionID uint32, floor uint64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.consumers {
		cs := &r.consumers[i]
		if cs.removed || cs.consumer == nil || cs.consumer.ID() != consumerID {
			continue
		}
		seq := cs.cursorByPartition[partitionID]
		if ok && floor < seq {
			seq = floor
		}
		if err := r.cursorStore.SaveCursor(context.Background(), consumerID, partitionID, seq); err != nil {
			slog.Warn("router: failed to save cursor after floor release",
				"consumer", consumerID,
				"partition", partitionID,
				"seq", seq,
				"err", err,
			)
		}
		return
	}
}
