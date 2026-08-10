package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

// defaultMaxIndexKeys bounds the number of (table, key) entries the indexed
// watermark path will hold in memory for a single table's active snapshot.
//
// Worst-case memory for one table's index is roughly
// maxIndexKeys * (avg key size + 8 bytes for the uint64 LSN), plus Go map
// overhead (~50 bytes/entry). At the default of 2,000,000 keys and a
// generous 128-byte average canonical key, that is ~2M * (128+8+50) ≈ 372MB
// worst case for a single table with that many keys mutated *during* its
// snapshot window. If a table's index would exceed this cap, ShouldEmit
// falls back to the original full-partition scan for that table (see
// tableIndex.overCap) rather than growing the map unbounded.
const defaultMaxIndexKeys = 2_000_000

// watermarkPageSize is the number of partition entries fetched per ReadPartition
// call, used both by the legacy full-scan fallback and by the one-time index
// build in StartTable. Paging ensures a superseding WAL event is never missed
// no matter how many events a partition has accumulated.
const watermarkPageSize = 10000

// tableIndex is the in-memory index for one table's active snapshot: for
// every (table, key) pair with an event LSN greater than snapshotLSN, it
// records the maximum such LSN. Only entries with lsn > snapshotLSN can ever
// change a ShouldEmit decision (see ShouldEmit's doc comment), so entries at
// or below snapshotLSN are never indexed.
type tableIndex struct {
	snapshotLSN uint64
	keys        map[string]uint64 // canonical key bytes (as string) -> max LSN > snapshotLSN
	// overCap is set once keys would exceed the checker's maxIndexKeys cap.
	// keys is nilled out at that point (release memory) and ShouldEmit falls
	// back to the full-partition scan for this table for the rest of its
	// snapshot, exactly matching pre-indexed behaviour (correct, just slow).
	overCap bool
}

// WatermarkChecker determines whether a snapshot row should be emitted by
// checking whether a more recent WAL event for the same (table, pk) exists
// in the Event Log. This enforces the watermark deduplication invariant:
// a snapshot read is dropped if a WAL event with a higher LSN already exists.
//
// Two lookup paths are supported:
//
//   - Indexed (preferred): StartTable(table, snapshotLSN) builds an in-memory
//     map[key]maxLSN once for the whole table by scanning every partition a
//     single time, then keeps the map current via a synchronous EventLog
//     append observer (see AppendObserver / AppendObservable in
//     internal/eventlog). ShouldEmit becomes an O(1) map lookup. Call
//     FinishTable when the table's snapshot completes to release the memory.
//   - Scan (fallback): the original implementation, used automatically when
//     StartTable was never called for a table, when the table's index
//     exceeded the memory cap, or when the underlying EventLog does not
//     support AppendObservable (e.g. the NATS cluster-mode EventLog). Pages
//     through the entire partition for the row's key on every call —
//     correct but O(partition size) per row.
type WatermarkChecker struct {
	eventLog      eventlog.EventLog
	numPartitions uint32
	maxIndexKeys  int

	mu     sync.Mutex
	tables map[string]*tableIndex

	unregister func() // detaches this checker from the EventLog's observer list; nil if unsupported
}

// NewWatermarkChecker creates a WatermarkChecker backed by the given EventLog.
// numPartitions must match the EventLog's partition count.
//
// If el implements eventlog.AppendObservable (BadgerEventLog does; the NATS
// cluster EventLog currently does not), the checker registers itself as an
// AppendObserver immediately — before any table's index is ever built — so
// the "observer attached before initial scan" ordering required for
// correctness (see StartTable) holds for every table, from the very first
// StartTable call onward.
func NewWatermarkChecker(el eventlog.EventLog, numPartitions uint32) *WatermarkChecker {
	w := &WatermarkChecker{
		eventLog:      el,
		numPartitions: numPartitions,
		maxIndexKeys:  defaultMaxIndexKeys,
		tables:        make(map[string]*tableIndex),
	}
	if obsAble, ok := el.(eventlog.AppendObservable); ok {
		w.unregister = obsAble.RegisterObserver(w)
	}
	return w
}

// SetMaxIndexKeys overrides the per-table memory cap (default
// defaultMaxIndexKeys). Values <= 0 are ignored (the current cap is kept) —
// there is intentionally no "unlimited" setting, since that would defeat the
// cap's purpose of bounding worst-case memory (see defaultMaxIndexKeys).
// Must be called before StartTable for the new cap to apply to that table.
func (w *WatermarkChecker) SetMaxIndexKeys(n int) {
	if n <= 0 {
		return
	}
	w.mu.Lock()
	w.maxIndexKeys = n
	w.mu.Unlock()
}

// Close detaches this checker from the EventLog's observer list. Safe to
// call multiple times or when no observer was ever registered.
func (w *WatermarkChecker) Close() {
	if w.unregister != nil {
		w.unregister()
	}
}

// StartTable builds (or rebuilds) the in-memory watermark index for table,
// enabling the O(1) indexed ShouldEmit path for it. Call this once at the
// start of a table's snapshot, before the first snapshot row is read.
//
// Ordering (correctness-critical, BKF-02): this method registers the empty
// index for table BEFORE scanning the event log. Since NewWatermarkChecker
// already registered this checker as an AppendObserver ahead of any
// StartTable call, any event committed after the index is registered here is
// guaranteed to be captured by ONE OF:
//
//  1. ObserveAppend, if the commit's synchronous observer callback (which
//     always runs after the Badger commit, per AppendObserver's contract)
//     happens to run after this method's initial map insert, or
//  2. The scan started immediately below, which reads Badger's then-current
//     state and therefore sees anything committed before the scan starts
//     (including anything committed between the map insert and the scan, by
//     transitivity: commit-before-observer-call and, separately here,
//     insert-before-scan-start are both single-goroutine program-order facts).
//
// An event can be observed by BOTH paths (e.g. committed while the scan is
// mid-flight); that is harmless because indexing is idempotent — merging the
// same (key, lsn) twice just re-writes the same max. The only failure mode
// this ordering rules out is an event being seen by NEITHER path, which
// would silently let a superseded snapshot row escape suppression.
//
// If the resulting index would exceed the configured memory cap, StartTable
// aborts the build and marks the table as over-cap: ShouldEmit falls back to
// the scan-based implementation for this table (see tableIndex.overCap).
//
// If the scan itself fails or is cancelled partway through (ctx.Err() or a
// ReadPartition error), StartTable removes the partial index it had
// registered before returning the error. A partial index is worse than no
// index: unlike the intentional overCap fallback, ShouldEmit has no way to
// tell a partial index from a complete one, so leaving it registered would
// make ShouldEmit silently trust incomplete data — a WAL event in a
// not-yet-scanned partition would be invisible to it, exactly the "seen by
// NEITHER path" failure mode the ordering argument above is meant to rule
// out. Removing it forces every ShouldEmit call for this table back onto the
// always-correct scan path until a subsequent StartTable call succeeds.
func (w *WatermarkChecker) StartTable(ctx context.Context, table string, snapshotLSN uint64) (err error) {
	ti := &tableIndex{
		snapshotLSN: snapshotLSN,
		keys:        make(map[string]uint64),
	}

	// Register the (empty) index BEFORE scanning. From this point on,
	// ObserveAppend (already live since NewWatermarkChecker) will merge any
	// matching event into ti.keys as it commits.
	w.mu.Lock()
	w.tables[table] = ti
	w.mu.Unlock()

	defer func() {
		if err == nil {
			return
		}
		w.mu.Lock()
		// Only remove it if it's still OUR ti — a concurrent StartTable call
		// for the same table (or FinishTable) may have already replaced or
		// removed it, and we must not clobber that newer state.
		if w.tables[table] == ti {
			delete(w.tables, table)
		}
		w.mu.Unlock()
	}()

	for partition := uint32(0); partition < w.numPartitions; partition++ {
		fromSeq := uint64(0)
		for {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			entries, readErr := w.eventLog.ReadPartition(ctx, partition, fromSeq, watermarkPageSize)
			if readErr != nil {
				return fmt.Errorf("watermark: build index for %q, partition %d: %w", table, partition, readErr)
			}

			w.mergeEntries(table, ti, entries)

			w.mu.Lock()
			overCap := ti.overCap
			w.mu.Unlock()
			if overCap {
				return nil // cap hit; ShouldEmit will use the scan fallback for this table
			}

			if len(entries) < watermarkPageSize {
				break
			}
			fromSeq = entries[len(entries)-1].Seq + 1
		}
	}

	return nil
}

// FinishTable releases the in-memory index for table (if any), bounding
// memory to only the tables currently being backfilled. Safe to call even if
// StartTable was never called for table, or was already finished.
func (w *WatermarkChecker) FinishTable(table string) {
	w.mu.Lock()
	delete(w.tables, table)
	w.mu.Unlock()
}

// ShouldEmit returns true if the snapshot row for (table, pk) should be emitted.
//
// It returns false if any entry in the event log for the same (table, pk) has
// an LSN greater than snapshotLSN — meaning a WAL event has already superseded
// this snapshot row.
//
// If StartTable(table, snapshotLSN) was previously called (and the table's
// index has not exceeded the memory cap), this is an O(1) map lookup.
// Otherwise it transparently falls back to the O(partition size) full scan —
// the original implementation — so callers that never adopt StartTable (or
// whose EventLog does not support AppendObservable) keep working unchanged.
func (w *WatermarkChecker) ShouldEmit(ctx context.Context, table string, pk json.RawMessage, snapshotLSN uint64) (bool, error) {
	w.mu.Lock()
	ti, ok := w.tables[table]
	if ok && !ti.overCap && ti.snapshotLSN == snapshotLSN {
		_, superseded := ti.keys[string(pk)]
		w.mu.Unlock()
		return !superseded, nil
	}
	w.mu.Unlock()

	return w.shouldEmitScan(ctx, table, pk, snapshotLSN)
}

// ObserveAppend implements eventlog.AppendObserver. It is called synchronously
// by the EventLog after every successful Append/AppendBatch (see that
// interface's doc comment for the durability/ordering contract this relies
// on). Events for tables with no active index (StartTable never called, or
// already over-cap) are skipped cheaply.
func (w *WatermarkChecker) ObserveAppend(evs []*event.ChangeEvent, _ []uint64) {
	if len(evs) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ev := range evs {
		ti, ok := w.tables[ev.Table]
		if !ok || ti.overCap {
			continue
		}
		w.mergeEventLocked(ti, ev)
	}
}

// mergeEntries merges a page of LogEntry results into ti.keys, filtering to
// entries for table with lsn > ti.snapshotLSN, and enforces the cap.
// Acquires w.mu itself (safe to call without holding the lock).
func (w *WatermarkChecker) mergeEntries(table string, ti *tableIndex, entries []eventlog.LogEntry) {
	if len(entries) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// The index may have already been replaced (e.g. a concurrent StartTable
	// for the same table) or hit its cap while we were reading this page;
	// re-check under the lock before mutating.
	if current := w.tables[table]; current != ti || ti.overCap {
		return
	}
	for _, entry := range entries {
		if err := entry.MaterializeEvent(); err != nil {
			continue
		}
		ev := entry.Event
		if ev.Table != table {
			continue
		}
		w.mergeEventLocked(ti, ev)
		if ti.overCap {
			return
		}
	}
}

// mergeEventLocked merges a single event into ti.keys if its LSN supersedes
// ti.snapshotLSN, tracking the maximum LSN seen per key. Enforces
// w.maxIndexKeys, setting ti.overCap and releasing ti.keys if exceeded.
// Callers must hold w.mu.
func (w *WatermarkChecker) mergeEventLocked(ti *tableIndex, ev *event.ChangeEvent) {
	lsn, err := lsnFromMetadata(ev)
	if err != nil {
		// Conservative: an unparsable LSN is skipped, matching the scan
		// path's behaviour of skipping entries it can't parse.
		return
	}
	if lsn <= ti.snapshotLSN {
		return
	}
	key := string(ev.Key)
	if existing, found := ti.keys[key]; !found || lsn > existing {
		if !found && len(ti.keys) >= w.maxIndexKeys {
			ti.overCap = true
			ti.keys = nil
			return
		}
		ti.keys[key] = lsn
	}
}

// shouldEmitScan is the original scan-based implementation, kept as the
// fallback path for tables with no active index (StartTable not called),
// tables that exceeded the memory cap, and EventLog backends that do not
// support AppendObservable.
//
// The partition is computed via eventlog.PartitionOf(pk, numPartitions) to
// avoid scanning all partitions. The partition is paged through to
// completion: a single capped read would miss the newest (highest-seq)
// events, which are exactly the ones most likely to supersede the snapshot
// row (BKF-02).
func (w *WatermarkChecker) shouldEmitScan(ctx context.Context, table string, pk json.RawMessage, snapshotLSN uint64) (bool, error) {
	partition := eventlog.PartitionOf(pk, w.numPartitions)

	fromSeq := uint64(0)
	for {
		entries, err := w.eventLog.ReadPartition(ctx, partition, fromSeq, watermarkPageSize)
		if err != nil {
			return false, fmt.Errorf("watermark: read partition %d: %w", partition, err)
		}

		for _, entry := range entries {
			if err := entry.MaterializeEvent(); err != nil {
				continue
			}
			ev := entry.Event
			if ev.Table != table {
				continue
			}
			if string(ev.Key) != string(pk) {
				continue
			}

			lsn, err := lsnFromMetadata(ev)
			if err != nil {
				// If we can't parse the LSN, skip this entry conservatively
				continue
			}
			if lsn > snapshotLSN {
				return false, nil
			}
		}

		// Fewer than a full page means the partition is exhausted.
		if len(entries) < watermarkPageSize {
			break
		}
		// Advance past the last entry read (ReadPartition is fromSeq-inclusive).
		fromSeq = entries[len(entries)-1].Seq + 1
	}

	return true, nil
}

// lsnFromMetadata extracts the LSN uint64 from a ChangeEvent's metadata["lsn"].
// The lsn field is stored as a string like "0/1A2B3C4".
func lsnFromMetadata(ev *event.ChangeEvent) (uint64, error) {
	raw, ok := ev.Metadata["lsn"]
	if !ok {
		return 0, fmt.Errorf("watermark: no lsn in metadata")
	}
	lsnStr, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("watermark: lsn is not a string: %T", raw)
	}
	lsn, err := pglogrepl.ParseLSN(lsnStr)
	if err != nil {
		return 0, fmt.Errorf("watermark: parse lsn %q: %w", lsnStr, err)
	}
	return uint64(lsn), nil
}
