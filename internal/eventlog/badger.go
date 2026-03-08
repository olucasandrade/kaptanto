package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/kaptanto/kaptanto/internal/event"
)

// BadgerEventLog is the BadgerDB-backed implementation of EventLog.
// It is safe for sequential calls from a single goroutine. Callers must
// serialize concurrent Append calls externally.
type BadgerEventLog struct {
	db            *badger.DB
	seqs          []*badger.Sequence
	numPartitions uint32
	retention     time.Duration
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

	return &BadgerEventLog{
		db:            db,
		seqs:          seqs,
		numPartitions: numPartitions,
		retention:     retention,
	}, nil
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
	partition := partitionOf(ev.Key, b.numPartitions)

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

	return seq, nil
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

			var ev event.ChangeEvent
			if err := json.Unmarshal(val, &ev); err != nil {
				return fmt.Errorf("eventlog: unmarshal event at partition %d: %w", partition, err)
			}

			_, seq := decodePartKey(item.KeyCopy(nil))
			entries = append(entries, LogEntry{Seq: seq, Event: &ev})
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

// partitionOf returns the partition index for the given groupingKey using FNV-1a.
// The grouping key is the raw JSON bytes of the event's primary key (ev.Key).
// This is deterministic across restarts because Key is deterministic.
func partitionOf(groupingKey []byte, numPartitions uint32) uint32 {
	h := fnv.New32a()
	h.Write(groupingKey)
	return h.Sum32() % numPartitions
}
