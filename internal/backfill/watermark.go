package backfill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pglogrepl"
	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
)

// WatermarkChecker determines whether a snapshot row should be emitted by
// checking whether a more recent WAL event for the same (table, pk) exists
// in the Event Log. This enforces the watermark deduplication invariant:
// a snapshot read is dropped if a WAL event with a higher LSN already exists.
type WatermarkChecker struct {
	eventLog      eventlog.EventLog
	numPartitions uint32
}

// NewWatermarkChecker creates a WatermarkChecker backed by the given EventLog.
// numPartitions must match the EventLog's partition count.
func NewWatermarkChecker(el eventlog.EventLog, numPartitions uint32) *WatermarkChecker {
	return &WatermarkChecker{
		eventLog:      el,
		numPartitions: numPartitions,
	}
}

// ShouldEmit returns true if the snapshot row for (table, pk) should be emitted.
//
// It returns false if any entry in the event log for the same (table, pk) has
// an LSN greater than snapshotLSN — meaning a WAL event has already superseded
// this snapshot row.
//
// The partition is computed via eventlog.PartitionOf(pk, numPartitions) to avoid
// scanning all partitions.
func (w *WatermarkChecker) ShouldEmit(ctx context.Context, table string, pk json.RawMessage, snapshotLSN uint64) (bool, error) {
	partition := eventlog.PartitionOf(pk, w.numPartitions)

	entries, err := w.eventLog.ReadPartition(ctx, partition, 0, 10000)
	if err != nil {
		return false, fmt.Errorf("watermark: read partition %d: %w", partition, err)
	}

	for _, entry := range entries {
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
