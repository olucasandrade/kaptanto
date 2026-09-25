package backfill_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkMongoEvent builds a ChangeEvent with Mongo-shaped metadata["lsn"]: the
// uint64 clusterTime encoding (T<<32|I) that captureSnapshotLSN / the Mongo
// normalizer stamp, rather than a Postgres LSN string.
func mkMongoEvent(table, keyJSON string, clusterLSN uint64) *event.ChangeEvent {
	return &event.ChangeEvent{
		Table:    table,
		Key:      json.RawMessage(keyJSON),
		Metadata: map[string]any{"lsn": clusterLSN, "snapshot": false, "collection": table},
	}
}

// TestWatermarkChecker_MongoMetadata_SuppressesStaleSnapshot asserts that
// ShouldEmit returns false when a newer same-key Mongo stream event (uint64
// clusterTime stamp) exists in the EventLog — the Mongo re-snapshot path
// that was previously a silent no-op because lsnFromMetadata only parsed
// Postgres LSN strings.
func TestWatermarkChecker_MongoMetadata_SuppressesStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	const numPartitions = uint32(64) // BKF-02: same partition count as EventLog
	el := newPartitionedEventLog(numPartitions)

	const table = "orders"
	key := `{"_id":{"$oid":"507f1f77bcf86cd799439011"}}`
	// snapshotLSN = clusterTime at snapshot start (T=1000, I=1).
	snapshotLSN := uint64(1000)<<32 | uint64(1)
	// Live stream event after snapshot start (T=1000, I=5) for the same key.
	liveLSN := uint64(1000)<<32 | uint64(5)

	_, err := el.Append(mkMongoEvent(table, key, liveLSN))
	require.NoError(t, err)

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	defer checker.Close()

	emit, err := checker.ShouldEmit(ctx, table, json.RawMessage(key), snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emit, "stale snapshot row must be suppressed when a newer same-key Mongo stream event exists")

	// Older stream event (below snapshotLSN) must not suppress.
	oldKey := `{"_id":{"$oid":"507f1f77bcf86cd799439012"}}`
	oldLSN := uint64(999)<<32 | uint64(1)
	_, err = el.Append(mkMongoEvent(table, oldKey, oldLSN))
	require.NoError(t, err)
	emit, err = checker.ShouldEmit(ctx, table, json.RawMessage(oldKey), snapshotLSN)
	require.NoError(t, err)
	assert.True(t, emit, "snapshot row must emit when only older stream events exist for the key")
}

// TestWatermarkChecker_MongoSnapshotLiveInterleave seeds a fake EventLog with
// a live Mongo stream event, then verifies both the scan path and the indexed
// StartTable path suppress the matching snapshot row (snapshot+live interleave).
func TestWatermarkChecker_MongoSnapshotLiveInterleave(t *testing.T) {
	ctx := context.Background()
	const numPartitions = uint32(64)
	el := newPartitionedEventLog(numPartitions)

	const table = "products"
	keyA := `{"_id":"a"}`
	keyB := `{"_id":"b"}`
	snapshotLSN := uint64(50)<<32 | uint64(0)
	liveLSN := uint64(50)<<32 | uint64(10) // supersedes snapshot for keyA

	_, err := el.Append(mkMongoEvent(table, keyA, liveLSN))
	require.NoError(t, err)
	// keyB has no live event — snapshot row should emit.

	// Scan path (no StartTable).
	scanChecker := backfill.NewWatermarkChecker(el, numPartitions)
	defer scanChecker.Close()
	emitA, err := scanChecker.ShouldEmit(ctx, table, json.RawMessage(keyA), snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emitA, "scan path: keyA superseded by live stream event")
	emitB, err := scanChecker.ShouldEmit(ctx, table, json.RawMessage(keyB), snapshotLSN)
	require.NoError(t, err)
	assert.True(t, emitB, "scan path: keyB has no superseding event")

	// Indexed path.
	idxChecker := backfill.NewWatermarkChecker(el, numPartitions)
	defer idxChecker.Close()
	require.NoError(t, idxChecker.StartTable(ctx, table, snapshotLSN))
	defer idxChecker.FinishTable(table)

	emitA, err = idxChecker.ShouldEmit(ctx, table, json.RawMessage(keyA), snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emitA, "indexed path: keyA superseded by live stream event")
	emitB, err = idxChecker.ShouldEmit(ctx, table, json.RawMessage(keyB), snapshotLSN)
	require.NoError(t, err)
	assert.True(t, emitB, "indexed path: keyB has no superseding event")

	// Live event appended after StartTable must still suppress via observer.
	keyC := `{"_id":"c"}`
	_, err = el.Append(mkMongoEvent(table, keyC, liveLSN))
	require.NoError(t, err)
	emitC, err := idxChecker.ShouldEmit(ctx, table, json.RawMessage(keyC), snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emitC, "indexed path: observer must pick up live append during snapshot")
}

// TestWatermarkChecker_OldMongoEntriesWithoutStamp_StayNonWatermarked documents
// the one-way migration: pre-stamp EventLog entries (no metadata["lsn"]) are
// skipped by extraction and cannot suppress a snapshot row.
func TestWatermarkChecker_OldMongoEntriesWithoutStamp_StayNonWatermarked(t *testing.T) {
	ctx := context.Background()
	const numPartitions = uint32(64)
	el := newPartitionedEventLog(numPartitions)

	const table = "orders"
	key := `{"_id":"legacy"}`
	_, err := el.Append(&event.ChangeEvent{
		Table: table,
		Key:   json.RawMessage(key),
		Metadata: map[string]any{
			"resume_token": "old",
			"snapshot":     false,
			// intentionally no "lsn" — pre-stamp Mongo event
		},
	})
	require.NoError(t, err)

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	defer checker.Close()
	emit, err := checker.ShouldEmit(ctx, table, json.RawMessage(key), 1)
	require.NoError(t, err)
	assert.True(t, emit, "old EventLog entries without the clusterTime stamp stay non-watermarked")
}
