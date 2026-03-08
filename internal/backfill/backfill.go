package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
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
