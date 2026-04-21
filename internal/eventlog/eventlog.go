// Package eventlog provides a durable, partitioned, deduplicated, TTL-expiring
// append-only event store built on dgraph-io/badger/v4.
//
// Every ChangeEvent parsed by the Postgres connector must be written here before
// the source LSN is acknowledged. This is the durability guarantee at the heart
// of kaptanto's crash-safety contract (CHK-01).
//
// Partitioning: events are assigned to partitions via FNV-1a hash of the primary
// key bytes modulo numPartitions. This is deterministic across restarts.
//
// Deduplication: before writing, the store checks a secondary dedup index keyed
// by event.IdempotencyKey. If the key exists, the write is silently skipped (LOG-03).
//
// TTL: both the partition entry and the dedup entry share the same retention TTL.
// Badger handles expiry transparently during LSM compaction (LOG-04).
package eventlog

import (
	"context"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// EventLog is the append-only durable event store interface.
// Implementations must be safe for sequential calls from a single goroutine.
// Callers must serialize concurrent Append calls externally if needed.
type EventLog interface {
	// Append durably writes ev to the event store and returns a monotonically
	// increasing sequence number for the event's partition.
	//
	// If ev.IdempotencyKey already exists in the store, the write is silently
	// skipped and seq=0 is returned as a sentinel value (LOG-03).
	//
	// The event is durable (fsync'd) before Append returns, satisfying LOG-01.
	Append(ev *event.ChangeEvent) (seq uint64, err error)

	// AppendBatch durably writes all events in evs within a single store
	// transaction. This amortises the per-transaction fsync cost across the
	// whole batch, which is critical on high-latency storage (e.g. Docker
	// Desktop virtiofs). Deduplication semantics are identical to Append: a
	// duplicate entry returns seq=0 for that position (LOG-03). The returned
	// slice has the same length as evs; position i corresponds to evs[i].
	//
	// CHK-01 ordering applies: callers must not advance the source LSN
	// checkpoint until AppendBatch returns without error.
	AppendBatch(evs []*event.ChangeEvent) (seqs []uint64, err error)

	// ReadPartition returns up to limit events from the given partition,
	// starting at fromSeq (inclusive), in ascending sequence order.
	//
	// Expired entries (past TTL) are automatically excluded by Badger.
	// Cancellation via ctx is respected between items.
	ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]LogEntry, error)

	// Close releases all partition sequences and closes the underlying store.
	// Must be called on graceful shutdown to avoid sequence number waste.
	Close() error
}

// LogEntry is a single event retrieved from ReadPartition.
type LogEntry struct {
	// Seq is the partition-local monotonically increasing sequence number.
	// Sequences are not gapless — gaps can appear after crashes (Badger sequences
	// pre-lease integers; leased-but-unused integers are lost on crash, expected).
	Seq uint64

	// PartitionID is the partition this entry was read from; set by ReadPartition.
	PartitionID uint32

	// Event is the deserialized ChangeEvent stored at this sequence position.
	Event *event.ChangeEvent
}
