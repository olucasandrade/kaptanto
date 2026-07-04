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

// PartitionNotifier provides per-partition notify channels so consumers can
// block instead of polling when no events are available. A depth-1 buffered
// channel is used per partition; the sender uses a non-blocking send so a
// slow or absent reader never stalls Append/AppendBatch (CHK-01 is safe).
//
// EventLog implementations that do not support notify (e.g. fakes in tests)
// need not implement this interface; Router falls back to timer-only polling
// when the EventLog does not implement PartitionNotifier.
type PartitionNotifier interface {
	// NotifyCh returns the notify channel for the given partition.
	// The returned channel is read-only; the implementation owns the write end.
	// Callers must never close the channel.
	NotifyCh(partition uint32) <-chan struct{}
}

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

// AppendObserver receives every successfully appended (non-duplicate) event,
// synchronously, immediately after the durable write commits and BEFORE
// Append/AppendBatch returns to its caller.
//
// This synchronous-before-return contract is load-bearing: WatermarkChecker
// (internal/backfill) uses it to build an incrementally-updated index instead
// of re-scanning the whole partition on every ShouldEmit call. For the index
// to be correct, an observer registered before a table's initial index scan
// begins must see every event committed after registration — a callback that
// fires only "eventually" (e.g. via an async queue) would let a snapshot row
// race ahead of the index and be wrongly emitted. See AppendObservable.
//
// Implementations must return quickly and must not block: this runs on the
// hot append path shared with the router and, transitively, source WAL/CDC
// ingestion (CHK-01). Do not perform I/O or acquire locks that could be held
// by a slow consumer.
type AppendObserver interface {
	// ObserveAppend is called once per Append/AppendBatch call with every
	// event that was actually written (duplicates are excluded). seqs[i]
	// corresponds to evs[i]; seqs are never 0 here (0 is the "duplicate"
	// sentinel used by Append/AppendBatch, and duplicates are filtered out
	// before observers are notified).
	ObserveAppend(evs []*event.ChangeEvent, seqs []uint64)
}

// AppendObservable is implemented by EventLog backends that support
// synchronous append observers (currently BadgerEventLog). Callers must type-
// assert an EventLog to this interface — it is optional so fakes used in
// tests need not implement it.
type AppendObservable interface {
	// RegisterObserver registers obs to be called synchronously on every
	// future successful Append/AppendBatch. It returns an unregister function
	// that removes obs; calling unregister more than once is a no-op.
	RegisterObserver(obs AppendObserver) (unregister func())
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

	// Raw holds the exact JSON bytes that were written to the store by Append.
	// Pass-through consumers (no column filter active for this event's table)
	// should write Raw directly to the wire instead of re-marshalling Event,
	// avoiding the unmarshal → re-marshal round-trip.
	//
	// Raw is always set by ReadPartition. Consumers must not modify the slice.
	Raw []byte
}
