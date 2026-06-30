package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

// BackfillConfig configures a single table's backfill strategy.
type BackfillConfig struct {
	// SourceID is the logical database connection identifier (e.g. "pg1").
	SourceID string
	// Schema is the Postgres schema (e.g. "public").
	Schema string
	// Table is the table name.
	Table string
	// Strategy controls the backfill mode:
	//   "snapshot_and_stream" — full snapshot, then live WAL
	//   "stream_only"         — no snapshot, just live WAL from now
	//   "snapshot_only"       — snapshot only, no WAL streaming
	//   "snapshot_deferred"   — record intent, snapshot on next restart
	//   "snapshot_partial"    — resume from last cursor if found
	Strategy string
	// PKCols is the ordered list of primary key column names.
	PKCols []string
	// NumPartitions must match the EventLog's partition count (default 64).
	NumPartitions uint32
}

// BackfillEngine coordinates the snapshot loop for one or more tables.
type BackfillEngine interface {
	// Run executes all pending backfills. For snapshot_deferred, it saves deferred
	// state and returns immediately. For stream_only, it is a no-op (state already marked completed).
	// Snapshot loops require a live pgx.Conn (injected via NewEngine).
	Run(ctx context.Context) error
	// HasPendingBackfills returns true if any configured table has a pending or
	// running backfill that has not yet completed.
	HasPendingBackfills() bool
}

// engine is the concrete BackfillEngine implementation.
type engine struct {
	configs  []BackfillConfig
	store    BackfillStore
	eventLog eventlog.EventLog
}

// NewEngine creates a BackfillEngine with the given configs, store, and optional event log.
// The eventLog is used for watermark checking during snapshot loops; pass nil for tests
// that do not exercise the snapshot loop.
func NewEngine(configs []BackfillConfig, store BackfillStore, el eventlog.EventLog) BackfillEngine {
	return &engine{
		configs:  configs,
		store:    store,
		eventLog: el,
	}
}

// HasPendingBackfills returns true if any table has a pending or running backfill.
//
// For stream_only: always false (no snapshot needed).
// For other strategies: true when no completed state is found.
func (e *engine) HasPendingBackfills() bool {
	ctx := context.Background()
	for _, cfg := range e.configs {
		if cfg.Strategy == "stream_only" {
			continue
		}
		state, err := e.store.LoadState(ctx, cfg.SourceID, cfg.Table)
		if err != nil {
			return true // conservative: assume pending on error
		}
		if state == nil {
			return true // no state = first run, pending
		}
		if state.Status == "pending" || state.Status == "running" {
			return true
		}
	}
	return false
}

// Run executes backfills according to each config's strategy.
//
//   - stream_only: saves completed state, returns immediately.
//   - snapshot_deferred: saves deferred state, returns immediately.
//   - others: executes the snapshot loop (requires live pgx.Conn — not wired here;
//     placeholder snapshotTable returns immediately for now).
func (e *engine) Run(ctx context.Context) error {
	for _, cfg := range e.configs {
		if err := e.runOne(ctx, cfg); err != nil {
			return fmt.Errorf("backfill: run %s/%s: %w", cfg.SourceID, cfg.Table, err)
		}
	}
	return nil
}

func (e *engine) runOne(ctx context.Context, cfg BackfillConfig) error {
	switch cfg.Strategy {
	case "stream_only":
		// Mark completed immediately — no snapshot needed.
		return e.store.SaveState(ctx, &BackfillState{
			SourceID:  cfg.SourceID,
			Table:     cfg.Table,
			Status:    "completed",
			Strategy:  cfg.Strategy,
			UpdatedAt: time.Now(),
		})

	case "snapshot_deferred":
		// Record intent; snapshot will be executed on the next configured trigger.
		return e.store.SaveState(ctx, &BackfillState{
			SourceID:  cfg.SourceID,
			Table:     cfg.Table,
			Status:    "deferred",
			Strategy:  cfg.Strategy,
			UpdatedAt: time.Now(),
		})

	default:
		// snapshot_and_stream, snapshot_only, snapshot_partial
		return e.snapshotTable(ctx, cfg)
	}
}

// snapshotTable runs the keyset-cursor snapshot loop for a single table.
// It requires a live database connection; in Plan 04-02 the PostgresConnector
// will inject a pgx.Conn. For now the loop is stubbed — the full implementation
// is wired in the next plan.
func (e *engine) snapshotTable(ctx context.Context, cfg BackfillConfig) error {
	// Stub: full snapshot loop wired in 04-02 when pgx.Conn is available.
	return nil
}

// ---------------------------------------------------------------------------
// BackfillEngineImpl — production implementation with pgx.Conn + AppendFn
// ---------------------------------------------------------------------------

// backfillBatchSize is the number of snapshot rows accumulated before a single
// AppendBatch call. Mirrors the WAL path constant (1024) but smaller to bound
// peak memory during initial load while still amortising per-fsync overhead.
const backfillBatchSize = 256

// AppendFn is the function BackfillEngineImpl calls to deliver each snapshot
// event. In production this is connector.AppendAndQueue. In tests it can be a
// mock.
type AppendFn func(ctx context.Context, ev *event.ChangeEvent) error

// AppendBatchFn is the function BackfillEngineImpl calls to deliver a batch of
// snapshot events in a single durable write. In production this is
// connector.AppendAndQueueBatch. In tests it can be a fake.
type AppendBatchFn func(ctx context.Context, evs []*event.ChangeEvent) error

// OpenConnFn opens a pgx.Conn for snapshot SELECT queries. The backfill engine
// is NOT allowed to use the replication connection.
type OpenConnFn func(ctx context.Context) (*pgx.Conn, error)

// BackfillEngineImpl is the production BackfillEngine. It receives its
// dependencies at construction time and implements the full keyset-cursor
// snapshot loop.
type BackfillEngineImpl struct {
	configs        []BackfillConfig
	store          BackfillStore
	idGen          *event.IDGenerator
	appendFn       AppendFn
	appendBatchFn  AppendBatchFn
	openConnFn     OpenConnFn
	// watermark is optional; nil disables watermark deduplication.
	watermark *WatermarkChecker
}

// NewBackfillEngine creates a BackfillEngineImpl with the given dependencies.
// appendFn must not be nil. appendBatchFn must not be nil. openConnFn must not be nil.
// watermark may be nil (disables per-row watermark deduplication).
func NewBackfillEngine(
	configs []BackfillConfig,
	store BackfillStore,
	idGen *event.IDGenerator,
	appendFn AppendFn,
	openConnFn OpenConnFn,
) *BackfillEngineImpl {
	return &BackfillEngineImpl{
		configs:    configs,
		store:      store,
		idGen:      idGen,
		appendFn:   appendFn,
		openConnFn: openConnFn,
	}
}

// NewBackfillEngineWithBatch creates a BackfillEngineImpl that uses batched
// appends for snapshot rows (AppendBatchFn). The per-event appendFn is still
// used for the snapshot_complete control event.
func NewBackfillEngineWithBatch(
	configs []BackfillConfig,
	store BackfillStore,
	idGen *event.IDGenerator,
	appendFn AppendFn,
	appendBatchFn AppendBatchFn,
	openConnFn OpenConnFn,
) *BackfillEngineImpl {
	return &BackfillEngineImpl{
		configs:       configs,
		store:         store,
		idGen:         idGen,
		appendFn:      appendFn,
		appendBatchFn: appendBatchFn,
		openConnFn:    openConnFn,
	}
}

// SetWatermark sets an optional WatermarkChecker used to skip snapshot rows
// that have already been superseded by a WAL event.
func (b *BackfillEngineImpl) SetWatermark(wc *WatermarkChecker) {
	b.watermark = wc
}

// HasPendingBackfills returns true if any configured table has a pending or
// running backfill.
func (b *BackfillEngineImpl) HasPendingBackfills() bool {
	ctx := context.Background()
	for _, cfg := range b.configs {
		if cfg.Strategy == "stream_only" {
			continue
		}
		state, err := b.store.LoadState(ctx, cfg.SourceID, cfg.Table)
		if err != nil {
			slog.Error("backfill: HasPendingBackfills: load state", "error", err,
				"source", cfg.SourceID, "table", cfg.Table)
			return false
		}
		if state == nil {
			return true // first run
		}
		if state.Status == "pending" || state.Status == "running" {
			return true
		}
	}
	return false
}

// Run executes all pending backfills in order.
func (b *BackfillEngineImpl) Run(ctx context.Context) error {
	for _, cfg := range b.configs {
		if err := b.runOne(ctx, cfg); err != nil {
			return fmt.Errorf("backfill: run %s/%s: %w", cfg.SourceID, cfg.Table, err)
		}
	}
	return nil
}

func (b *BackfillEngineImpl) runOne(ctx context.Context, cfg BackfillConfig) error {
	state, err := b.store.LoadState(ctx, cfg.SourceID, cfg.Table)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil {
		// First run: initialise state.
		state = &BackfillState{
			SourceID:  cfg.SourceID,
			Table:     cfg.Table,
			Status:    "pending",
			Strategy:  cfg.Strategy,
			UpdatedAt: time.Now(),
		}
	}

	switch cfg.Strategy {
	case "stream_only":
		state.Status = "completed"
		state.UpdatedAt = time.Now()
		return b.store.SaveState(ctx, state)

	case "snapshot_deferred":
		state.Status = "deferred"
		state.UpdatedAt = time.Now()
		return b.store.SaveState(ctx, state)

	default:
		// snapshot_and_stream, snapshot_only, snapshot_partial
		if state.Status == "completed" {
			return nil
		}
		if err := b.snapshotTable(ctx, cfg, state); err != nil {
			return err
		}
		// Emit snapshot_complete control event.
		snapshotID := fmt.Sprintf("%s_%s_%d", cfg.SourceID, cfg.Table, time.Now().UnixNano())
		controlEv := MakeControlEvent(b.idGen, cfg.SourceID, cfg.Table, "snapshot_complete", snapshotID, state)
		if err := b.appendFn(ctx, controlEv); err != nil {
			return fmt.Errorf("emit snapshot_complete control event: %w", err)
		}
		state.Status = "completed"
		state.UpdatedAt = time.Now()
		return b.store.SaveState(ctx, state)
	}
}

// snapshotTable runs the full keyset-cursor snapshot loop using a dedicated
// pgx.Conn opened via openConnFn.
//
//nolint:gocyclo // the keyset-snapshot loop interleaves cursor paging, watermark checks, and BKF-02 handling; splitting it would obscure the single-pass invariant. Tracked for incremental refactor.
func (b *BackfillEngineImpl) snapshotTable(ctx context.Context, cfg BackfillConfig, state *BackfillState) error {
	conn, err := b.openConnFn(ctx)
	if err != nil {
		return fmt.Errorf("open snapshot connection: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Estimate total rows from pg_class statistics.
	var reltuples int64
	err = conn.QueryRow(ctx,
		`SELECT GREATEST(reltuples::bigint, 0) FROM pg_class WHERE relname = $1`,
		cfg.Table,
	).Scan(&reltuples)
	if err != nil {
		// Non-fatal: unknown total rows.
		reltuples = -1
	}
	if reltuples > 0 {
		state.TotalRows = reltuples
	} else {
		state.TotalRows = -1
	}

	// Initialise keyset cursor, restoring position from saved state.
	cursor := &KeysetCursor{
		Table:  cfg.Table,
		Schema: cfg.Schema,
		PKCols: cfg.PKCols,
	}
	if state.CursorKey != nil {
		var lastPK []any
		if jsonErr := json.Unmarshal(state.CursorKey, &lastPK); jsonErr == nil {
			cursor.LastPK = lastPK
		}
	}

	optimizer := NewBatchOptimizer()

	// Generate a stable snapshotID for all rows in this run.
	snapshotID := fmt.Sprintf("%s_%s_%d", cfg.SourceID, cfg.Table, time.Now().UnixNano())

	state.Status = "running"
	if err := b.store.SaveState(ctx, state); err != nil {
		return fmt.Errorf("save running state: %w", err)
	}

	// BKF-02: assign SnapshotLSN on first start only (not on crash-resume).
	// Without this, state.SnapshotLSN=0 and ShouldEmit(lsn > 0) suppresses every
	// row that has any WAL activity, inverting watermark deduplication semantics.
	if state.SnapshotLSN == 0 {
		var flushLSNStr string
		if qErr := conn.QueryRow(ctx, "SELECT pg_current_wal_flush_lsn()").Scan(&flushLSNStr); qErr == nil {
			if lsn, parseErr := pglogrepl.ParseLSN(flushLSNStr); parseErr == nil {
				state.SnapshotLSN = uint64(lsn)
			}
		}
		// Non-fatal: if query or parse fails, SnapshotLSN remains 0 (conservative
		// path — no rows suppressed incorrectly). pg_current_wal_flush_lsn() returns
		// NULL on standby; the nil scan error is caught here and handled safely.
	}

	// eventBuf accumulates snapshot row events for batched appends (Fix: BKF batch).
	// Allocated once and reused across pages; reset to [:0] after each flush.
	var eventBuf []*event.ChangeEvent
	// bufLastPKValues tracks the PK of the last event currently in eventBuf so
	// the cursor can be advanced to the correct position after each flush.
	var bufLastPKValues []any

	for {
		batchSize := optimizer.Current()
		var sql string
		var args []any
		if cursor.LastPK == nil {
			sql, args = cursor.BuildFirstQuery(batchSize)
		} else {
			sql, args = cursor.BuildNextQuery(batchSize)
		}

		batchStart := time.Now()
		rows, queryErr := conn.Query(ctx, sql, args...)
		if queryErr != nil {
			return fmt.Errorf("snapshot query: %w", queryErr)
		}

		// Field descriptions, column names, and PK-column positions are constant
		// for the entire result set — compute them once per page instead of
		// reallocating a map and re-deriving column-name strings on every row.
		fieldDescs := rows.FieldDescriptions()
		colNames := make([]string, len(fieldDescs))
		for i, fd := range fieldDescs {
			colNames[i] = string(fd.Name)
		}
		pkColIdx := make(map[string]int, len(cfg.PKCols))
		for i, col := range cfg.PKCols {
			pkColIdx[col] = i
		}

		var rowCount int
		var lastPKValues []any

		for rows.Next() {
			values, valErr := rows.Values()
			if valErr != nil {
				rows.Close()
				return fmt.Errorf("scan row values: %w", valErr)
			}

			// Build column-name → value map for JSON marshalling.
			rowMap := make(map[string]any, len(colNames))
			pkValues := make([]any, len(cfg.PKCols))
			pkMap := make(map[string]any, len(cfg.PKCols))

			for i, colName := range colNames {
				rowMap[colName] = values[i]
				if idx, ok := pkColIdx[colName]; ok {
					pkValues[idx] = values[i]
					pkMap[colName] = values[i]
				}
			}

			rowJSON, marshalErr := json.Marshal(rowMap)
			if marshalErr != nil {
				rows.Close()
				return fmt.Errorf("marshal row: %w", marshalErr)
			}
			pkJSON, marshalErr := json.Marshal(pkMap)
			if marshalErr != nil {
				rows.Close()
				return fmt.Errorf("marshal pk: %w", marshalErr)
			}

			// Watermark check: skip rows superseded by a WAL event.
			if b.watermark != nil {
				emit, wErr := b.watermark.ShouldEmit(ctx, cfg.Table, pkJSON, state.SnapshotLSN)
				if wErr != nil {
					slog.Warn("backfill: watermark check error", "error", wErr)
					// Conservative: emit the row.
				} else if !emit {
					lastPKValues = pkValues
					rowCount++
					continue
				}
			}

			readEv := MakeReadEvent(b.idGen, cfg.SourceID, cfg.Schema, cfg.Table,
				pkJSON, rowJSON, snapshotID, state)

			if b.appendBatchFn != nil {
				// Batched path: buffer the event; flush when the buffer is full.
				// BKF-03: cursor and state are only persisted after a successful flush.
				eventBuf = append(eventBuf, readEv)
				bufLastPKValues = pkValues // track PK of last buffered event
				if len(eventBuf) >= backfillBatchSize {
					if flushErr := b.flushEventBuf(ctx, eventBuf, state, cursor, bufLastPKValues); flushErr != nil {
						rows.Close()
						return flushErr
					}
					eventBuf = eventBuf[:0]
					bufLastPKValues = nil
				}
			} else {
				// Legacy single-event path (appendBatchFn not set).
				if appendErr := b.appendFn(ctx, readEv); appendErr != nil {
					rows.Close()
					return fmt.Errorf("append read event: %w", appendErr)
				}
			}

			lastPKValues = pkValues
			rowCount++
			state.ProcessedRows++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows error: %w", err)
		}

		// Flush any remaining buffered events for this page (BKF-03: persist
		// cursor only after the batch is durably written).
		if b.appendBatchFn != nil && len(eventBuf) > 0 {
			if flushErr := b.flushEventBuf(ctx, eventBuf, state, cursor, bufLastPKValues); flushErr != nil {
				return flushErr
			}
			eventBuf = eventBuf[:0]
			bufLastPKValues = nil
		}

		// Advance cursor and persist state (BKF-03 crash-resumable).
		// When appendBatchFn is set, flushEventBuf already persisted the cursor
		// for rows that were flushed mid-page; this call covers the final state
		// (ProcessedRows, status) after the page is fully processed.
		if b.appendBatchFn == nil {
			if lastPKValues != nil {
				cursor.LastPK = lastPKValues
				cursorJSON, marshalErr := json.Marshal(lastPKValues)
				if marshalErr == nil {
					state.CursorKey = cursorJSON
				}
			}
			if saveErr := b.store.SaveState(ctx, state); saveErr != nil {
				return fmt.Errorf("save state after batch: %w", saveErr)
			}
		} else {
			// Persist end-of-page state (status + ProcessedRows) even when
			// the batch path already advanced the cursor — this records the
			// updated ProcessedRows count for crash-resume accounting.
			if saveErr := b.store.SaveState(ctx, state); saveErr != nil {
				return fmt.Errorf("save state after batch: %w", saveErr)
			}
		}

		optimizer.Adjust(time.Since(batchStart))

		// Batch smaller than requested → last batch, snapshot complete.
		if rowCount < batchSize {
			break
		}
	}

	return nil
}

// flushEventBuf appends evs to the event log via appendBatchFn and then
// persists the cursor to the last seen PK values (BKF-03: cursor advances only
// after a successful durable write).
//
// lastPKValues is the PK tuple of the last row in evs. cursor and state are
// updated in-place on success.
func (b *BackfillEngineImpl) flushEventBuf(
	ctx context.Context,
	evs []*event.ChangeEvent,
	state *BackfillState,
	cursor *KeysetCursor,
	lastPKValues []any,
) error {
	if len(evs) == 0 {
		return nil
	}
	if err := b.appendBatchFn(ctx, evs); err != nil {
		return fmt.Errorf("append batch: %w", err)
	}
	// BKF-03: advance cursor only after the batch is durably written.
	if lastPKValues != nil {
		cursor.LastPK = lastPKValues
		cursorJSON, marshalErr := json.Marshal(lastPKValues)
		if marshalErr == nil {
			state.CursorKey = cursorJSON
		}
	}
	return nil
}

// MakeReadEvent constructs a ChangeEvent representing a single snapshot row read.
//
// EVT-03 contract:
//   - Operation == OpRead
//   - Before == nil
//   - After == rowJSON
//   - Key == pkJSON
//   - IdempotencyKey: "<sourceID>:<schema>.<table>:<pkJSON>:read:<snapshotID>"
//   - Metadata["snapshot"] == true
//   - Metadata["snapshot_id"] == snapshotID
//   - Metadata["snapshot_progress"] == {"total": state.TotalRows, "completed": state.ProcessedRows}
func MakeReadEvent(
	idGen *event.IDGenerator,
	sourceID, schema, table string,
	pkJSON, rowJSON json.RawMessage,
	snapshotID string,
	state *BackfillState,
) *event.ChangeEvent {
	qualifiedTable := table
	if schema != "" {
		qualifiedTable = schema + "." + table
	}

	idempotencyKey := fmt.Sprintf("%s:%s:%s:read:%s",
		sourceID, qualifiedTable, string(pkJSON), snapshotID)

	return &event.ChangeEvent{
		ID:             idGen.New(),
		IdempotencyKey: idempotencyKey,
		Timestamp:      time.Now(),
		Source:         sourceID,
		Operation:      event.OpRead,
		Schema:         schema,
		Table:          table,
		Key:            pkJSON,
		Before:         nil,
		After:          rowJSON,
		Metadata: map[string]any{
			"snapshot":    true,
			"snapshot_id": snapshotID,
			"snapshot_progress": map[string]any{
				"total":     state.TotalRows,
				"completed": state.ProcessedRows,
			},
		},
	}
}

// MakeControlEvent constructs a ChangeEvent representing a pipeline control signal.
//
// EVT-04 contract:
//   - Operation == OpControl
//   - Before == nil, After == nil
//   - Key == json.RawMessage(`{}`)
//   - Metadata["control_type"] == controlType
//   - Metadata["total_rows"] == state.ProcessedRows
//   - Metadata["snapshot_id"] == snapshotID
func MakeControlEvent(
	idGen *event.IDGenerator,
	sourceID, table, controlType string,
	snapshotID string,
	state *BackfillState,
) *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             idGen.New(),
		IdempotencyKey: fmt.Sprintf("%s:%s:control:%s:%s", sourceID, table, controlType, snapshotID),
		Timestamp:      time.Now(),
		Source:         sourceID,
		Operation:      event.OpControl,
		Table:          table,
		Key:            json.RawMessage(`{}`),
		Before:         nil,
		After:          nil,
		Metadata: map[string]any{
			"control_type": controlType,
			"total_rows":   state.ProcessedRows,
			"snapshot_id":  snapshotID,
		},
	}
}
