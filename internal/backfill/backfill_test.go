// Package backfill_test provides black-box TDD tests for the backfill engine.
// All tests use the external test package to ensure only exported symbols are tested.
package backfill_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/backfill"
	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- BKF-01: Keyset cursor queries ---

func TestKeysetCursor_FirstQuery_SinglePK(t *testing.T) {
	c := &backfill.KeysetCursor{
		Table:  "orders",
		Schema: "public",
		PKCols: []string{"id"},
	}
	sql, args := c.BuildFirstQuery(5000)
	assert.Equal(t, `SELECT * FROM public.orders ORDER BY id ASC LIMIT 5000`, sql)
	assert.Empty(t, args)
	assert.NotContains(t, sql, "OFFSET", "keyset cursor must never use OFFSET")
}

func TestKeysetCursor_NextQuery_SinglePK(t *testing.T) {
	c := &backfill.KeysetCursor{
		Table:  "orders",
		Schema: "public",
		PKCols: []string{"id"},
		LastPK: []any{99},
	}
	sql, args := c.BuildNextQuery(5000)
	assert.Equal(t, `SELECT * FROM public.orders WHERE id > $1 ORDER BY id ASC LIMIT 5000`, sql)
	assert.Equal(t, []any{99}, args)
	assert.NotContains(t, sql, "OFFSET")
}

func TestKeysetCursor_NextQuery_CompositePK(t *testing.T) {
	c := &backfill.KeysetCursor{
		Table:  "orders",
		Schema: "public",
		PKCols: []string{"tenant_id", "id"},
		LastPK: []any{"acme", 99},
	}
	sql, args := c.BuildNextQuery(500)
	assert.Equal(t, `SELECT * FROM public.orders WHERE (tenant_id, id) > ($1, $2) ORDER BY tenant_id ASC, id ASC LIMIT 500`, sql)
	assert.Equal(t, []any{"acme", 99}, args)
	assert.NotContains(t, sql, "OFFSET")
}

func TestKeysetCursor_NoSchemaQualification(t *testing.T) {
	c := &backfill.KeysetCursor{
		Table:  "orders",
		Schema: "",
		PKCols: []string{"id"},
	}
	sql, _ := c.BuildFirstQuery(100)
	assert.Equal(t, `SELECT * FROM orders ORDER BY id ASC LIMIT 100`, sql)
}

func TestKeysetCursor_NeverEmitsOFFSET(t *testing.T) {
	c := &backfill.KeysetCursor{
		Table:  "t",
		Schema: "",
		PKCols: []string{"a", "b"},
		LastPK: []any{1, 2},
	}
	first, _ := c.BuildFirstQuery(1000)
	next, _ := c.BuildNextQuery(1000)
	assert.NotContains(t, first, "OFFSET")
	assert.NotContains(t, next, "OFFSET")
}

// --- BKF-02: Watermark dedup ---

// mockEventLog is an in-memory EventLog for testing.
type mockEventLog struct {
	entries []eventlog.LogEntry
}

func (m *mockEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	return 0, nil
}

func (m *mockEventLog) ReadPartition(_ context.Context, partition uint32, fromSeq uint64, limit int) ([]eventlog.LogEntry, error) {
	return m.entries, nil
}

func (m *mockEventLog) Close() error { return nil }

func TestWatermarkChecker_ShouldEmit_NoEntries(t *testing.T) {
	mock := &mockEventLog{entries: nil}
	checker := backfill.NewWatermarkChecker(mock, 64)
	pk := json.RawMessage(`{"id":1}`)
	emit, err := checker.ShouldEmit(context.Background(), "orders", pk, 100)
	require.NoError(t, err)
	assert.True(t, emit, "should emit when no entries exist in partition")
}

func TestWatermarkChecker_ShouldEmit_AllEntriesLowerLSN(t *testing.T) {
	// Entry with lsn <= snapshotLSN: should still emit
	ev := &event.ChangeEvent{
		Table:    "orders",
		Key:      json.RawMessage(`{"id":1}`),
		Metadata: map[string]any{"lsn": "0/64"}, // 100 decimal
	}
	mock := &mockEventLog{entries: []eventlog.LogEntry{{Seq: 1, Event: ev}}}
	checker := backfill.NewWatermarkChecker(mock, 64)
	pk := json.RawMessage(`{"id":1}`)
	emit, err := checker.ShouldEmit(context.Background(), "orders", pk, 100)
	require.NoError(t, err)
	assert.True(t, emit, "should emit when all partition entries have LSN <= snapshotLSN")
}

func TestWatermarkChecker_ShouldEmit_SupersedingWALEntry(t *testing.T) {
	// LSN 200 = 0/C8 in hex
	ev := &event.ChangeEvent{
		Table:    "orders",
		Key:      json.RawMessage(`{"id":1}`),
		Metadata: map[string]any{"lsn": "0/C8"}, // 200 decimal
	}
	mock := &mockEventLog{entries: []eventlog.LogEntry{{Seq: 2, Event: ev}}}
	checker := backfill.NewWatermarkChecker(mock, 64)
	pk := json.RawMessage(`{"id":1}`)
	// snapshotLSN = 100, WAL entry LSN = 200 > 100 → should NOT emit
	emit, err := checker.ShouldEmit(context.Background(), "orders", pk, 100)
	require.NoError(t, err)
	assert.False(t, emit, "should not emit when a WAL entry with higher LSN exists")
}

func TestWatermarkChecker_ShouldEmit_DifferentTable(t *testing.T) {
	// Same key but different table: should not suppress
	ev := &event.ChangeEvent{
		Table:    "customers",
		Key:      json.RawMessage(`{"id":1}`),
		Metadata: map[string]any{"lsn": "0/C8"}, // 200 > 100
	}
	mock := &mockEventLog{entries: []eventlog.LogEntry{{Seq: 2, Event: ev}}}
	checker := backfill.NewWatermarkChecker(mock, 64)
	pk := json.RawMessage(`{"id":1}`)
	emit, err := checker.ShouldEmit(context.Background(), "orders", pk, 100)
	require.NoError(t, err)
	assert.True(t, emit, "should emit when superseding entry is for a different table")
}

// --- BKF-03: Crash-resumable state ---

func TestSQLiteBackfillStore_LoadState_FirstRun(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	state, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	assert.Nil(t, state, "first run should return nil, nil")
}

func TestSQLiteBackfillStore_SaveAndLoad(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	original := &backfill.BackfillState{
		SourceID:      "pg1",
		Table:         "orders",
		Status:        "running",
		Strategy:      "snapshot_and_stream",
		CursorKey:     []byte(`{"id":99}`),
		ProcessedRows: 5000,
		TotalRows:     100000,
		SnapshotLSN:   0x1A2B3C4,
	}

	err = store.SaveState(context.Background(), original)
	require.NoError(t, err)

	loaded, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, original.SourceID, loaded.SourceID)
	assert.Equal(t, original.Table, loaded.Table)
	assert.Equal(t, original.Status, loaded.Status)
	assert.Equal(t, original.Strategy, loaded.Strategy)
	assert.Equal(t, original.CursorKey, loaded.CursorKey)
	assert.Equal(t, original.ProcessedRows, loaded.ProcessedRows)
	assert.Equal(t, original.TotalRows, loaded.TotalRows)
	assert.Equal(t, original.SnapshotLSN, loaded.SnapshotLSN)
}

func TestSQLiteBackfillStore_Upsert(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	state := &backfill.BackfillState{
		SourceID:  "pg1",
		Table:     "orders",
		Status:    "running",
		Strategy:  "snapshot_and_stream",
		CursorKey: []byte(`{"id":50}`),
	}
	require.NoError(t, store.SaveState(context.Background(), state))

	state.CursorKey = []byte(`{"id":200}`)
	state.ProcessedRows = 10000
	state.Status = "completed"
	require.NoError(t, store.SaveState(context.Background(), state))

	loaded, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, []byte(`{"id":200}`), loaded.CursorKey)
	assert.Equal(t, int64(10000), loaded.ProcessedRows)
	assert.Equal(t, "completed", loaded.Status)
}

// --- BKF-04: Adaptive batch sizing ---

func TestBatchOptimizer_DefaultStart(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	assert.Equal(t, 5000, o.Current())
}

func TestBatchOptimizer_FastQuery_Grow(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	result := o.Adjust(500 * time.Millisecond)
	expected := min(int(float64(5000)*1.25), 50000)
	assert.Equal(t, expected, result, "fast query should grow batch by 25%%")
}

func TestBatchOptimizer_SlowQuery_3s_Shrink(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	result := o.Adjust(4 * time.Second)
	expected := max(5000/2, 100)
	assert.Equal(t, expected, result, "slow query (>3s) should halve batch size")
}

func TestBatchOptimizer_VerySlowQuery_6s_Shrink(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	result := o.Adjust(6 * time.Second)
	expected := max(5000/2, 100)
	assert.Equal(t, expected, result, "very slow query (>5s) should halve batch size")
}

func TestBatchOptimizer_NormalQuery_2s_NoChange(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	result := o.Adjust(2 * time.Second)
	assert.Equal(t, 5000, result, "normal query (1s-3s) should not change batch size")
}

func TestBatchOptimizer_MinCap(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	// Shrink repeatedly until floor
	for i := 0; i < 20; i++ {
		o.Adjust(6 * time.Second)
	}
	assert.Equal(t, 100, o.Current(), "batch size should not go below 100")
}

func TestBatchOptimizer_MaxCap(t *testing.T) {
	o := backfill.NewBatchOptimizer()
	// Grow repeatedly until ceiling
	for i := 0; i < 30; i++ {
		o.Adjust(100 * time.Millisecond)
	}
	assert.Equal(t, 50000, o.Current(), "batch size should not exceed 50000")
}

// --- BKF-05: Strategy handling ---

func TestBackfillEngine_StreamOnly_NoPending(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "stream_only",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	engine := backfill.NewEngine([]backfill.BackfillConfig{cfg}, store, nil)
	assert.False(t, engine.HasPendingBackfills(), "stream_only should have no pending backfills")
}

func TestBackfillEngine_SnapshotDeferred_SavesDeferred(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "snapshot_deferred",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	engine := backfill.NewEngine([]backfill.BackfillConfig{cfg}, store, nil)
	err = engine.Run(context.Background())
	require.NoError(t, err, "snapshot_deferred Run() should return immediately without error")

	state, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "deferred", state.Status)
}

func TestBackfillEngine_SnapshotAndStream_HasPending(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer store.Close()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "snapshot_and_stream",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	engine := backfill.NewEngine([]backfill.BackfillConfig{cfg}, store, nil)
	assert.True(t, engine.HasPendingBackfills(), "snapshot_and_stream should report pending backfills")
}

// --- EVT-03: Snapshot read event shape ---

func TestMakeReadEvent_Shape(t *testing.T) {
	idGen := event.NewIDGenerator()
	pkJSON := json.RawMessage(`{"id":1}`)
	rowJSON := json.RawMessage(`{"id":1,"name":"foo"}`)
	snapshotID := "snap_abc123"

	state := &backfill.BackfillState{
		TotalRows:     1000,
		ProcessedRows: 50,
	}

	ev := backfill.MakeReadEvent(idGen, "pg1", "public", "orders", pkJSON, rowJSON, snapshotID, state)

	assert.Equal(t, event.OpRead, ev.Operation)
	assert.Nil(t, ev.Before, "Before must be nil for snapshot reads")
	assert.Equal(t, rowJSON, ev.After)
	assert.Equal(t, pkJSON, ev.Key)
	assert.Contains(t, ev.IdempotencyKey, "pg1:public.orders:")
	assert.Contains(t, ev.IdempotencyKey, ":read:")
	assert.Contains(t, ev.IdempotencyKey, snapshotID)

	assert.True(t, ev.Metadata["snapshot"].(bool), "Metadata[snapshot] must be true")
	assert.NotEmpty(t, ev.Metadata["snapshot_id"], "Metadata[snapshot_id] must be set")

	progress, ok := ev.Metadata["snapshot_progress"].(map[string]any)
	require.True(t, ok, "snapshot_progress must be a map")
	assert.EqualValues(t, state.TotalRows, progress["total"])
	assert.EqualValues(t, state.ProcessedRows, progress["completed"])
}

// --- EVT-04: Control event shape ---

func TestMakeControlEvent_SnapshotComplete(t *testing.T) {
	idGen := event.NewIDGenerator()
	snapshotID := "snap_abc123"

	state := &backfill.BackfillState{
		ProcessedRows: 9999,
	}

	ev := backfill.MakeControlEvent(idGen, "pg1", "orders", "snapshot_complete", snapshotID, state)

	assert.Equal(t, event.OpControl, ev.Operation)
	assert.Nil(t, ev.Before)
	assert.Nil(t, ev.After)
	assert.Equal(t, json.RawMessage(`{}`), ev.Key)

	assert.Equal(t, "snapshot_complete", ev.Metadata["control_type"])
	assert.EqualValues(t, state.ProcessedRows, ev.Metadata["total_rows"])
	assert.NotEmpty(t, ev.Metadata["snapshot_id"])
}
