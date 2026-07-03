package backfill_test

// Tests for the BackfillEngineImpl.snapshotTable/flushEventBuf loop itself —
// the wiring between the keyset cursor, the watermark check, and the batched
// event append/cursor-persist sequence. Prior to this file, KeysetCursor and
// WatermarkChecker were only tested as isolated components (SQL-text
// generation, dedup logic); the engine loop that actually drives them against
// query results had 0% coverage. These tests exercise it via a fake
// backfill.SnapshotConn, made possible by the SnapshotConn interface seam.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConnRow implements pgx.Row. snapshotTable only uses QueryRow for the
// two non-fatal pre-loop lookups (pg_class reltuples, pg_current_wal_flush_lsn),
// both of which fall back safely on error — so every fake QueryRow call
// returns an error, keeping this fake minimal while exercising the real
// fallback paths (TotalRows=-1, SnapshotLSN stays 0).
type fakeConnRow struct{}

func (fakeConnRow) Scan(_ ...any) error { return errors.New("fakeConnRow: not implemented") }

// fakeConnRows implements pgx.Rows over a fixed, preloaded page of rows.
type fakeConnRows struct {
	cols []string
	rows [][]any
	idx  int
}

func (r *fakeConnRows) Close()                        {}
func (r *fakeConnRows) Err() error                    { return nil }
func (r *fakeConnRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeConnRows) RawValues() [][]byte           { return nil }
func (r *fakeConnRows) Conn() *pgx.Conn               { return nil }
func (r *fakeConnRows) Scan(_ ...any) error {
	return errors.New("fakeConnRows: Scan not implemented (snapshotTable uses Values)")
}

func (r *fakeConnRows) FieldDescriptions() []pgconn.FieldDescription {
	fds := make([]pgconn.FieldDescription, len(r.cols))
	for i, c := range r.cols {
		fds[i] = pgconn.FieldDescription{Name: c}
	}
	return fds
}

func (r *fakeConnRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeConnRows) Values() ([]any, error) {
	return r.rows[r.idx-1], nil
}

// fakeSnapshotConn implements backfill.SnapshotConn. pages are consumed in
// order, one per Query() call, so the sequence of pages drives multi-page
// pagination in the caller regardless of the LIMIT/OFFSET-free SQL text
// snapshotTable actually generates (which this fake never parses).
type fakeSnapshotConn struct {
	cols       []string
	pages      [][][]any
	pageIdx    int
	queryErr   error
	closeCalls int
}

func (c *fakeSnapshotConn) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fakeConnRow{}
}

func (c *fakeSnapshotConn) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if c.pageIdx >= len(c.pages) {
		return &fakeConnRows{cols: c.cols}, nil
	}
	page := c.pages[c.pageIdx]
	c.pageIdx++
	return &fakeConnRows{cols: c.cols, rows: page}, nil
}

func (c *fakeSnapshotConn) Close(_ context.Context) error {
	c.closeCalls++
	return nil
}

// idRows builds a page of single-column ("id") rows for ids [start, end).
func idRows(start, end int) [][]any {
	rows := make([][]any, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, []any{int64(i)})
	}
	return rows
}

// readEvents filters countingBatchLog.received down to OpRead snapshot-row
// events, excluding the single OpControl "snapshot_complete" event that Run
// also delivers through the same appendFn/appendBatchFn callbacks.
func readEvents(evs []*event.ChangeEvent) []*event.ChangeEvent {
	out := make([]*event.ChangeEvent, 0, len(evs))
	for _, ev := range evs {
		if ev.Operation == event.OpRead {
			out = append(out, ev)
		}
	}
	return out
}

// TestSnapshotTable_MultiPage_PaginatesAndPersistsCursor drives snapshotTable
// across two pages (5000 rows, then 3 more — the first page sized to exactly
// the initial BatchOptimizer batch size so the loop must fetch a second page
// to discover it's the last one) and asserts every row is delivered exactly
// once, in order, and the store's persisted cursor lands on the very last PK
// — proving the keyset-cursor-to-query-to-append-to-persisted-state wiring,
// not just each piece in isolation.
func TestSnapshotTable_MultiPage_PaginatesAndPersistsCursor(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "snapshot_and_stream",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	conn := &fakeSnapshotConn{
		cols: []string{"id"},
		pages: [][][]any{
			idRows(1, 5001),    // page 1: ids 1..5000 (== initial batch size 5000)
			idRows(5001, 5004), // page 2: ids 5001..5003 (< batch size => last page)
		},
	}

	idGen := event.NewIDGenerator()
	bl := &countingBatchLog{}
	eng := backfill.NewBackfillEngineWithBatch(
		[]backfill.BackfillConfig{cfg}, store, idGen,
		bl.appendSingle, bl.appendBatch,
		func(_ context.Context) (backfill.SnapshotConn, error) { return conn, nil },
	)

	require.NoError(t, eng.Run(context.Background()))

	got := readEvents(bl.received)
	assert.Len(t, got, 5003, "every row across both pages must be delivered exactly once")
	assert.Equal(t, 1, conn.closeCalls, "snapshot connection must be closed after the run")

	state, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "completed", state.Status)

	var lastPK []any
	require.NoError(t, json.Unmarshal(state.CursorKey, &lastPK))
	require.Len(t, lastPK, 1)
	assert.InEpsilon(t, float64(5003), lastPK[0], 0.0001,
		"persisted cursor must land on the PK of the very last row across both pages")
}

// TestSnapshotTable_WatermarkDropsSupersededRow_BKF02 verifies the engine
// loop actually consults the WatermarkChecker per row (not just that
// WatermarkChecker.ShouldEmit works in isolation): a row whose PK has a
// superseding WAL event in the EventLog must be silently skipped while its
// page-mates are still emitted.
func TestSnapshotTable_WatermarkDropsSupersededRow_BKF02(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "snapshot_and_stream",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	// snapshotTable's pg_current_wal_flush_lsn lookup always errors in this
	// fake (fakeConnRow), so SnapshotLSN falls back to 0 — any WAL event with
	// lsn > 0 therefore supersedes. Row id=2's canonical PK, per pk.Canonical,
	// is the JSON string `{"id":"2"}` (int64 -> decimal string).
	superseding := &event.ChangeEvent{
		Table:    "orders",
		Key:      json.RawMessage(`{"id":"2"}`),
		Metadata: map[string]any{"lsn": "0/C8"}, // 200 decimal, > snapshotLSN(0)
	}
	mockLog := &mockEventLog{entries: []eventlog.LogEntry{{Seq: 1, Event: superseding}}}

	conn := &fakeSnapshotConn{
		cols:  []string{"id"},
		pages: [][][]any{idRows(1, 4)}, // ids 1, 2, 3 — id=2 is watermarked
	}

	idGen := event.NewIDGenerator()
	bl := &countingBatchLog{}
	eng := backfill.NewBackfillEngineWithBatch(
		[]backfill.BackfillConfig{cfg}, store, idGen,
		bl.appendSingle, bl.appendBatch,
		func(_ context.Context) (backfill.SnapshotConn, error) { return conn, nil },
	)
	eng.SetWatermark(backfill.NewWatermarkChecker(mockLog, 64))

	require.NoError(t, eng.Run(context.Background()))

	got := readEvents(bl.received)
	require.Len(t, got, 2, "the watermarked row must be dropped, its two page-mates emitted")
	var gotIDs []string
	for _, ev := range got {
		gotIDs = append(gotIDs, string(ev.Key))
	}
	assert.ElementsMatch(t, []string{`{"id":"1"}`, `{"id":"3"}`}, gotIDs,
		"emitted rows must be exactly ids 1 and 3, never the watermarked id 2")
}

// TestSnapshotTable_FlushFailure_DoesNotAdvancePersistedCursor verifies
// BKF-03: when a mid-page batch flush fails, the persisted store state must
// not reflect any progress made by that failed flush (or any flush after
// it) — only a fully successful page's end-of-page SaveState may advance the
// durable cursor. This is what "cursor advances only after a durable write"
// actually means at the engine-loop level, not just inside flushEventBuf in
// isolation.
func TestSnapshotTable_FlushFailure_DoesNotAdvancePersistedCursor(t *testing.T) {
	store, err := backfill.OpenSQLiteBackfillStore(t.TempDir() + "/backfill.db")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	cfg := backfill.BackfillConfig{
		SourceID:      "pg1",
		Schema:        "public",
		Table:         "orders",
		Strategy:      "snapshot_and_stream",
		PKCols:        []string{"id"},
		NumPartitions: 64,
	}

	// 512 rows on a single page: backfillBatchSize (256) triggers a first
	// mid-page flush at row 256, then a second at end-of-page for the
	// remaining 256. Fail on the 2nd flush call.
	conn := &fakeSnapshotConn{
		cols:  []string{"id"},
		pages: [][][]any{idRows(1, 513)},
	}

	idGen := event.NewIDGenerator()
	bl := &countingBatchLog{errOnCall: 2}
	eng := backfill.NewBackfillEngineWithBatch(
		[]backfill.BackfillConfig{cfg}, store, idGen,
		bl.appendSingle, bl.appendBatch,
		func(_ context.Context) (backfill.SnapshotConn, error) { return conn, nil },
	)

	runErr := eng.Run(context.Background())
	require.Error(t, runErr, "Run must surface the flush failure")

	require.Len(t, bl.batchCalls, 1, "only the first (successful) flush should have completed")
	assert.Equal(t, 256, bl.batchCalls[0])

	state, err := store.LoadState(context.Background(), "pg1", "orders")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Nil(t, state.CursorKey,
		"persisted cursor must remain nil: the successful flush only updated an "+
			"in-memory cursor/state that snapshotTable never got to persist before "+
			"the second flush failed and aborted the page")
}
