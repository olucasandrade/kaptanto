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
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

const pollInterval = 10 * time.Millisecond

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
// (e.g. http.Flusher) over an entire batch, significantly increasing SSE
// delivery throughput on high-latency transports.
type BatchFlusher interface {
	// FlushBatch flushes any buffered writes to the underlying transport.
	// Called by runPartition after processing each batch of entries.
	// Errors are logged but do not block future delivery.
	FlushBatch(ctx context.Context) error
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

// consumerState tracks per-consumer runtime state: the last successfully
// delivered seq per partition. Blocked message group state is owned by
// RetryScheduler (rs field on Router), not by consumerState.
type consumerState struct {
	consumer          Consumer
	cursorByPartition map[uint32]uint64
}

// consumerSnap is a lightweight snapshot of a consumer's state captured at
// the start of dispatch. It is reused across events via per-partition scratch
// buffers to avoid per-event heap allocations.
type consumerSnap struct {
	consumer Consumer
	blocked  bool
	cursor   uint64 // this consumer's next-seq for the partition at snapshot time
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
	ownedPartitions []uint32 // nil = all partitions (non-cluster default)
}

// NewRouter creates a new Router. If cs is nil, an in-memory noopCursorStore
// is used (delivery positions are not persisted across restarts).
func NewRouter(el eventlog.EventLog, numPartitions uint32, cs ConsumerCursorStore) *Router {
	if cs == nil {
		cs = NewNoopCursorStore()
	}
	return &Router{
		eventLog:      el,
		numPartitions: numPartitions,
		cursorStore:   cs,
		rs:            NewRetryScheduler(),
	}
}

// SetMetrics injects a KaptantoMetrics reference for ConsumerLag reporting.
// Call after construction, before Run.
func (r *Router) SetMetrics(m *observability.KaptantoMetrics) {
	r.metrics = m
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

// Register adds a Consumer to the Router. Register must be called before Run.
// The initial delivery cursor for each partition is loaded from the
// ConsumerCursorStore.
func (r *Router) Register(c Consumer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cs := consumerState{
		consumer:          c,
		cursorByPartition: make(map[uint32]uint64),
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

// runPartition is the per-partition poll loop. It reads events sequentially
// and dispatches each to all registered consumers. On empty batch it sleeps
// pollInterval before retrying. On ReadPartition error it logs and retries —
// it never exits early due to errors.
//
// Each goroutine owns its own snaps and deliveryErrs scratch slices that are
// grown as needed and reset ([:0]) per event. This eliminates the two
// per-event heap allocations that the previous dispatch signature caused.
func (r *Router) runPartition(ctx context.Context, partitionID uint32) {
	// Per-partition scratch buffers — owned exclusively by this goroutine.
	// Grown on demand; reset to [:0] before each dispatch call.
	var snaps []consumerSnap
	var deliveryErrs []error

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nextSeq := r.minCursorForPartition(partitionID)

		entries, err := r.eventLog.ReadPartition(ctx, partitionID, nextSeq, 256)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("router: ReadPartition error", "partition", partitionID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
			continue
		}

		if len(entries) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
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

		// Fix E: flush once per batch for consumers that implement BatchFlusher.
		// Acquiring RLock to snapshot consumer list is safe here; the flush
		// itself happens outside the lock.
		r.mu.RLock()
		flushers := make([]BatchFlusher, 0, len(r.consumers))
		for _, cs := range r.consumers {
			if bf, ok := cs.consumer.(BatchFlusher); ok {
				flushers = append(flushers, bf)
			}
		}
		r.mu.RUnlock()
		for _, bf := range flushers {
			if ctx.Err() != nil {
				return
			}
			if err := bf.FlushBatch(ctx); err != nil {
				slog.Warn("router: batch flush error", "partition", partitionID, "err", err)
			}
		}
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
		groupKey = string(entry.Event.Key)
	}

	// Phase 1: snapshot consumer list and blocked state under RLock.
	// r.consumers only grows (no Unregister), so indices captured here
	// remain valid for Phase 3's write lock.
	r.mu.RLock()
	n := len(r.consumers)
	// Grow scratch slices if needed; reuse existing capacity otherwise.
	snaps := (*snapsPtr)[:0]
	if cap(snaps) < n {
		snaps = make([]consumerSnap, n)
	} else {
		snaps = snaps[:n]
	}
	for i, cs := range r.consumers {
		blocked := false
		if hasBlocked {
			// IsBlocked acquires its own mutex — safe to call under RLock.
			blocked = r.rs.IsBlocked(cs.consumer.ID(), groupKey)
		}
		snaps[i] = consumerSnap{
			consumer: cs.consumer,
			blocked:  blocked,
			cursor:   cs.cursorByPartition[partitionID],
		}
	}
	r.mu.RUnlock()
	*snapsPtr = snaps

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
		// Clear stale errors from previous event.
		for i := range errs {
			errs[i] = nil
		}
	}
	for i, snap := range snaps {
		if snap.blocked || ctx.Err() != nil {
			continue
		}
		if entry.Seq < snap.cursor {
			continue // already delivered and acked by this consumer
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
				groupKey = string(entry.Event.Key)
				break
			}
		}
	}

	// Phase 3: update in-memory cursors and persist them under write lock.
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, snap := range snaps {
		if i >= len(r.consumers) {
			break // guard against consumers added between Phase 1 and Phase 3
		}
		cs := &r.consumers[i]

		if snap.blocked {
			if r.metrics != nil {
				r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Add(1)
			}
			continue
		}

		// Skipped in Phase 2 because this consumer already acked entry.Seq.
		// Do not touch the cursor — advancing toward entry.Seq+1 would regress it.
		if entry.Seq < snap.cursor {
			continue
		}

		if err := errs[i]; err != nil {
			slog.Warn("router: delivery failed, blocking message group",
				"consumer", cs.consumer.ID(),
				"key", groupKey,
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
			continue
		}

		// Advance cursor to entry.Seq+1 (the next seq to read from).
		// Cursor semantics: the value stored is the NEXT seq to read, so that
		// minCursorForPartition can be passed directly to ReadPartition as fromSeq.
		nextForPartition := entry.Seq + 1
		if nextForPartition > cs.cursorByPartition[partitionID] {
			cs.cursorByPartition[partitionID] = nextForPartition
		}
		// Best-effort cursor persistence; failures are logged only.
		if err := r.cursorStore.SaveCursor(ctx, cs.consumer.ID(), partitionID, nextForPartition); err != nil {
			slog.Warn("router: failed to save cursor",
				"consumer", cs.consumer.ID(),
				"partition", partitionID,
				"seq", entry.Seq,
				"err", err,
			)
		}
		if r.metrics != nil {
			r.metrics.ConsumerLag.WithLabelValues(cs.consumer.ID()).Set(0)
		}
	}
}
