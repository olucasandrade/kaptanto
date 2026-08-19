package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/olucasandrade/kaptanto/internal/event"
)

// BadgerEventLog is the BadgerDB-backed implementation of EventLog.
// It is safe for sequential calls from a single goroutine. Callers must
// serialize concurrent Append calls externally.
//
// It also implements PartitionNotifier: each partition has a depth-1 buffered
// notify channel that is pulsed (non-blocking send) after every successful
// Append/AppendBatch write. Readers (e.g. Router.runPartition) can select on
// the channel instead of polling, eliminating the idle 10ms poll floor.
type BadgerEventLog struct {
	db            *badger.DB
	seqs          []*badger.Sequence
	numPartitions uint32
	retention     time.Duration
	notifyChs     []chan struct{} // one depth-1 buffered channel per partition

	obsMu     sync.RWMutex
	observers map[int]AppendObserver // keyed by monotonically increasing id for stable unregister
	nextObsID int
}

// Open creates or reopens a BadgerEventLog at dir.
//
// numPartitions controls how many partitions are created (recommended: 64).
// retention is the TTL for all entries; events are automatically expired by Badger.
//
// Suppress all Badger logger output (WithLogger(nil)) — kaptanto uses slog.
func Open(dir string, numPartitions uint32, retention time.Duration) (*BadgerEventLog, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(nil) // suppress Badger's internal INFO/DEBUG logs (pitfall 5)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open badger: %w", err)
	}

	// Allocate one badger.Sequence per partition. Bandwidth=256 means Badger
	// pre-leases 256 integers before persisting the high-watermark to disk.
	// Sequences survive restarts; up to 255 integers may be lost on crash (expected,
	// sequences do not need to be gapless — pitfall 3).
	seqs := make([]*badger.Sequence, numPartitions)
	for i := uint32(0); i < numPartitions; i++ {
		key := fmt.Appendf(nil, "seq:p:%d", i)
		seq, err := db.GetSequence(key, 256)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("eventlog: get sequence for partition %d: %w", i, err)
		}
		// Badger sequences start at 0. We reserve 0 as the "duplicate detected"
		// sentinel returned by Append when an idempotency key is already present.
		// Consuming 0 here ensures the first real Append always returns seq >= 1.
		if _, err := seq.Next(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("eventlog: advance sequence past zero for partition %d: %w", i, err)
		}
		seqs[i] = seq
	}

	// Allocate one depth-1 buffered notify channel per partition.
	notifyChs := make([]chan struct{}, numPartitions)
	for i := uint32(0); i < numPartitions; i++ {
		notifyChs[i] = make(chan struct{}, 1)
	}

	return &BadgerEventLog{
		db:            db,
		seqs:          seqs,
		numPartitions: numPartitions,
		retention:     retention,
		notifyChs:     notifyChs,
		observers:     make(map[int]AppendObserver),
	}, nil
}

// RegisterObserver implements AppendObservable. obs is called synchronously,
// after the durable write commits, on every future Append/AppendBatch that
// writes at least one non-duplicate event — including calls already in
// flight when RegisterObserver is invoked, since registration and the
// observer dispatch loop both hold obsMu (see notifyObservers).
func (b *BadgerEventLog) RegisterObserver(obs AppendObserver) (unregister func()) {
	b.obsMu.Lock()
	id := b.nextObsID
	b.nextObsID++
	b.observers[id] = obs
	b.obsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.obsMu.Lock()
			delete(b.observers, id)
			b.obsMu.Unlock()
		})
	}
}

// notifyObservers dispatches evs/seqs (already filtered to non-duplicates by
// the caller) to every registered observer, synchronously and in the calling
// goroutine — this is what makes RegisterObserver's "sees every future
// commit" guarantee hold: the write is durable in Badger before this runs,
// and this runs before Append/AppendBatch returns.
func (b *BadgerEventLog) notifyObservers(evs []*event.ChangeEvent, seqs []uint64) {
	if len(evs) == 0 {
		return
	}
	b.obsMu.RLock()
	defer b.obsMu.RUnlock()
	for _, obs := range b.observers {
		obs.ObserveAppend(evs, seqs)
	}
}

// NotifyCh returns the read-only notify channel for the given partition.
// It implements PartitionNotifier. The channel carries at most one pending
// signal (depth-1 buffer); a non-blocking send coalesces concurrent writes
// so the reader is woken at least once without the sender ever blocking.
func (b *BadgerEventLog) NotifyCh(partition uint32) <-chan struct{} {
	return b.notifyChs[partition]
}

// notify pulses the notify channel for partition using a non-blocking send.
// If the channel already has a pending signal (buffer full), the call is a
// no-op — the reader will still wake up, satisfying the "at least once" goal
// without ever blocking the caller (CHK-01 safety).
func (b *BadgerEventLog) notify(partition uint32) {
	select {
	case b.notifyChs[partition] <- struct{}{}:
	default:
	}
}

// Append durably writes ev to the event store (LOG-01).
//
// Partitioning is by FNV-1a hash of ev.Key bytes modulo numPartitions (LOG-02).
//
// If ev.IdempotencyKey already exists, the write is silently skipped and seq=0
// is returned as a "duplicate detected" sentinel (LOG-03).
//
// Both the partition entry and the dedup entry receive the same TTL (LOG-04,
// pitfall 4: dedup TTL must not be shorter than partition TTL).
//
// IMPORTANT: seq.Next() is called OUTSIDE the Badger transaction to avoid holding
// the sequence lock inside a read-write transaction. Gaps in sequence numbers are
// acceptable (anti-pattern note from research).
func (b *BadgerEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	partition := PartitionOf(ev.Key, b.numPartitions)

	val, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("eventlog: marshal event: %w", err)
	}

	dedupKey := encodeDedupKey(ev.IdempotencyKey)

	// Get the next sequence number BEFORE entering the transaction.
	// This avoids holding the sequence lease inside the MVCC transaction window,
	// reducing conflict risk. A crash between Next() and SetEntry wastes one
	// sequence number — acceptable (sequences need not be gapless).
	seq, err := b.seqs[partition].Next()
	if err != nil {
		return 0, fmt.Errorf("eventlog: sequence for partition %d: %w", partition, err)
	}

	var dupDetected bool
	err = b.db.Update(func(txn *badger.Txn) error {
		// Dedup check: if the idempotency key already exists, skip the write (LOG-03).
		if _, err := txn.Get(dedupKey); err == nil {
			dupDetected = true
			return nil
		} else if err != badger.ErrKeyNotFound {
			return fmt.Errorf("eventlog: dedup check: %w", err)
		}

		partKey := encodePartKey(partition, seq)

		// Write partition entry with TTL (LOG-01, LOG-04).
		partEntry := badger.NewEntry(partKey, val).WithTTL(b.retention)
		if err := txn.SetEntry(partEntry); err != nil {
			return fmt.Errorf("eventlog: set partition entry: %w", err)
		}

		// Write dedup entry with the SAME TTL as partition entry (pitfall 4).
		// Value encodes (partition, seq) for future reverse lookup.
		dedupEntry := badger.NewEntry(dedupKey, encodePartSeq(partition, seq)).WithTTL(b.retention)
		if err := txn.SetEntry(dedupEntry); err != nil {
			return fmt.Errorf("eventlog: set dedup entry: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	if dupDetected {
		// Return seq=0 as "already existed" sentinel. Callers that need to distinguish
		// a duplicate from a first write can check seq==0. This is documented behavior.
		return 0, nil
	}

	// Pulse the per-partition notify channel so a waiting runPartition wakes
	// immediately rather than waiting for the fallback timer. Non-blocking: a
	// slow reader never stalls the writer (CHK-01 preserved).
	b.notify(partition)

	// Notify observers (e.g. WatermarkChecker's index) synchronously, after
	// the durable commit and before returning, so registered observers never
	// miss an event (see AppendObserver's documented contract).
	b.notifyObservers([]*event.ChangeEvent{ev}, []uint64{seq})

	return seq, nil
}

// preparedEvent holds data allocated outside the Badger transaction for one
// event in an AppendBatch call.
type preparedEvent struct {
	partition uint32
	val       []byte
	dedupKey  []byte
	seq       uint64
}

// AppendBatch durably writes all events in evs, amortising fsync cost over the
// whole logical batch.
//
// Phase 1: all per-event data (JSON marshal, sequence number) is prepared
// outside the Badger transaction to avoid holding sequence locks inside the
// MVCC window (same reasoning as Append). Sequence number gaps on crash are
// acceptable (sequences need not be gapless — pitfall 3).
//
// Phase 2: the logical batch is committed in one or more Badger transactions.
// Badger caps a transaction by byte size (MaxBatchSize) and entry count
// (MaxBatchCount). A single wide-row batch would otherwise fail with
// ErrTxnTooBig (LOG-05). Because callers serialize Append/AppendBatch and
// because every event carries an idempotency key, chunking is safe:
// - duplicates inside the batch are still skipped (LOG-03);
// - if a later chunk fails, earlier committed chunks are idempotent on retry
//   (CHK-01/BKF-03: checkpoints/cursors only advance after a successful call).
func (b *BadgerEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	if len(evs) == 0 {
		return nil, nil
	}

	seqs := make([]uint64, len(evs)) // zero-initialised; 0 = duplicate sentinel

	// Phase 1: marshal and allocate sequence numbers before the transaction.
	items := make([]preparedEvent, len(evs))
	for i, ev := range evs {
		partition := PartitionOf(ev.Key, b.numPartitions)
		val, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("eventlog: marshal event[%d]: %w", i, err)
		}
		seq, err := b.seqs[partition].Next()
		if err != nil {
			return nil, fmt.Errorf("eventlog: sequence for partition %d: %w", partition, err)
		}
		items[i] = preparedEvent{
			partition: partition,
			val:       val,
			dedupKey:  encodeDedupKey(ev.IdempotencyKey),
			seq:       seq,
		}
	}

	// Phase 2: split the prepared batch into Badger-sized chunks and commit each.
	ranges := b.chunkRanges(items)
	for ci, r := range ranges {
		if err := b.commitChunk(items, r.start, r.end, seqs); err != nil {
			return nil, fmt.Errorf("eventlog: append batch chunk %d/%d: %w", ci+1, len(ranges), err)
		}
	}

	// Pulse notify channels for every partition that received at least one new
	// (non-duplicate) event. Coalescing via the depth-1 buffer means we call
	// notify at most once per partition regardless of how many events landed there.
	// Also collect the non-duplicate subset to hand to observers (LOG-03: seq==0
	// marks a duplicate that was silently skipped and must not be observed).
	notified := make(map[uint32]bool, len(items))
	writtenEvs := make([]*event.ChangeEvent, 0, len(evs))
	writtenSeqs := make([]uint64, 0, len(evs))
	for i, item := range items {
		if seqs[i] != 0 {
			if !notified[item.partition] {
				b.notify(item.partition)
				notified[item.partition] = true
			}
			writtenEvs = append(writtenEvs, evs[i])
			writtenSeqs = append(writtenSeqs, seqs[i])
		}
	}

	// Notify observers synchronously, after the durable commit and before
	// returning (see AppendObserver's documented contract).
	b.notifyObservers(writtenEvs, writtenSeqs)

	return seqs, nil
}

// chunkRange is a half-open [start,end) slice of prepared items.
type chunkRange struct {
	start int
	end   int
}

// chunkRanges splits prepared events into ranges that each fit within Badger's
// per-transaction byte and entry-count budgets. We leave 50% headroom because
// Badger's skiplist node overhead is not part of MaxBatchSize/MaxBatchCount.
func (b *BadgerEventLog) chunkRanges(items []preparedEvent) []chunkRange {
	if len(items) == 0 {
		return nil
	}

	maxSize := b.db.MaxBatchSize() / 2
	maxCount := b.db.MaxBatchCount() / 2
	if maxSize <= 0 {
		maxSize = 1
	}
	if maxCount <= 0 {
		maxCount = 1
	}

	var ranges []chunkRange
	start := 0
	var size int64
	var count int64

	for i, item := range items {
		// Two entries per event: partition entry + dedup entry.
		itemSize := int64(len(item.val) + len(item.dedupKey) +
			len(encodePartKey(item.partition, item.seq)) +
			len(encodePartSeq(item.partition, item.seq)))
		itemCount := int64(2)

		// Start a new chunk when the current one would exceed either budget.
		// A single oversized item is still emitted in its own range so the
		// caller receives a clean error from Badger (or from commitChunk's
		// pre-check) rather than an infinite loop.
		if i > start && (size+itemSize > maxSize || count+itemCount > maxCount) {
			ranges = append(ranges, chunkRange{start: start, end: i})
			start = i
			size = 0
			count = 0
		}
		size += itemSize
		count += itemCount
	}
	if start < len(items) {
		ranges = append(ranges, chunkRange{start: start, end: len(items)})
	}
	return ranges
}

// commitChunk writes items[start:end] to Badger. seqs is updated in-place for
// non-duplicate events at their original indices.
func (b *BadgerEventLog) commitChunk(items []preparedEvent, start, end int, seqs []uint64) error {
	hardMaxSize := b.db.MaxBatchSize()

	return b.db.Update(func(txn *badger.Txn) error {
		for i := start; i < end; i++ {
			item := items[i]

			// Guard: an event that by itself exceeds Badger's hard transaction
			// budget can never be written. Badger would return ErrTxnTooBig; we
			// fail fast with a clearer message.
			itemSize := int64(len(item.val) + len(item.dedupKey) +
				len(encodePartKey(item.partition, item.seq)) +
				len(encodePartSeq(item.partition, item.seq)))
			if itemSize > hardMaxSize {
				return fmt.Errorf("eventlog: event exceeds maximum transaction size (%d > %d)", itemSize, hardMaxSize)
			}

			// Dedup check: skip if idempotency key already exists (LOG-03).
			if _, err := txn.Get(item.dedupKey); err == nil {
				continue // duplicate; seqs[i] stays 0
			} else if err != badger.ErrKeyNotFound {
				return fmt.Errorf("eventlog: dedup check[%d]: %w", i, err)
			}

			partKey := encodePartKey(item.partition, item.seq)

			partEntry := badger.NewEntry(partKey, item.val).WithTTL(b.retention)
			if err := txn.SetEntry(partEntry); err != nil {
				return fmt.Errorf("eventlog: set partition entry[%d]: %w", i, err)
			}

			dedupEntry := badger.NewEntry(item.dedupKey, encodePartSeq(item.partition, item.seq)).WithTTL(b.retention)
			if err := txn.SetEntry(dedupEntry); err != nil {
				return fmt.Errorf("eventlog: set dedup entry[%d]: %w", i, err)
			}

			seqs[i] = item.seq
		}
		return nil
	})
}

// ReadPartition returns up to limit events from partition, starting at fromSeq (inclusive),
// in ascending sequence order. Expired entries are automatically excluded by Badger.
// Cancellation via ctx is respected between items.
func (b *BadgerEventLog) ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]LogEntry, error) {
	prefix := encodePartPrefix(partition)
	startKey := encodePartKey(partition, fromSeq)

	var entries []LogEntry
	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(startKey); it.ValidForPrefix(prefix) && len(entries) < limit; it.Next() {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			item := it.Item()

			// ValueCopy returns nil for expired items with Badger's native TTL;
			// the iterator itself skips expired keys, so this is a safety measure.
			val, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("eventlog: read value at partition %d: %w", partition, err)
			}

			_, seq := decodePartKey(item.KeyCopy(nil))
			// Raw preserves stored bytes for pass-through consumers; Event is lazy-
			// decoded via LogEntry.MaterializeEvent when a consumer needs fields
			// beyond the grouping key (fix-plan: badger-readpartition-eager-unmarshal).
			entries = append(entries, LogEntry{Seq: seq, PartitionID: partition, Raw: val})
		}
		return nil
	})
	return entries, err
}

// Close releases all partition sequences and closes the underlying Badger database.
// Must be called on graceful shutdown. Calling seq.Release() before db.Close()
// flushes leased integers back to Badger, reducing wasted sequence numbers on restart
// (pitfall 6).
func (b *BadgerEventLog) Close() error {
	for _, seq := range b.seqs {
		_ = seq.Release() // best-effort flush; ignore errors (Release is idempotent)
	}
	return b.db.Close()
}

// Ping checks that the Badger database is open and responsive.
// It runs a no-op read transaction — the standard Badger liveness check.
func (b *BadgerEventLog) Ping() error {
	return b.db.View(func(txn *badger.Txn) error { return nil })
}

// PartitionOf returns the partition index for the given groupingKey using FNV-1a.
// The grouping key is the raw JSON bytes of the event's primary key (ev.Key).
// This is deterministic across restarts because Key is deterministic.
func PartitionOf(groupingKey []byte, numPartitions uint32) uint32 {
	h := fnv.New32a()
	h.Write(groupingKey)
	return h.Sum32() % numPartitions
}
