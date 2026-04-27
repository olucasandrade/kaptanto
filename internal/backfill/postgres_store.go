package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Compile-time assertion: PostgresBackfillStore must satisfy BackfillStore.
var _ BackfillStore = (*PostgresBackfillStore)(nil)

const createPostgresBackfillTableSQL = `
CREATE TABLE IF NOT EXISTS kaptanto_backfill_states (
    source_id       TEXT        NOT NULL,
    table_name      TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending',
    strategy        TEXT        NOT NULL,
    cursor_key      BYTEA,
    total_rows      BIGINT      DEFAULT 0,
    processed_rows  BIGINT      DEFAULT 0,
    snapshot_lsn    BIGINT      DEFAULT 0,
    started_at      TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (source_id, table_name)
);`

const upsertPostgresBackfillStateSQL = `
INSERT INTO kaptanto_backfill_states
    (source_id, table_name, status, strategy, cursor_key,
     total_rows, processed_rows, snapshot_lsn, started_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())
ON CONFLICT (source_id, table_name) DO UPDATE SET
    status         = EXCLUDED.status,
    strategy       = EXCLUDED.strategy,
    cursor_key     = EXCLUDED.cursor_key,
    total_rows     = EXCLUDED.total_rows,
    processed_rows = EXCLUDED.processed_rows,
    snapshot_lsn   = EXCLUDED.snapshot_lsn,
    started_at     = EXCLUDED.started_at,
    updated_at     = NOW();`

const selectPostgresBackfillStateSQL = `
SELECT source_id, table_name, status, strategy, cursor_key,
       total_rows, processed_rows, snapshot_lsn, started_at, updated_at
FROM kaptanto_backfill_states
WHERE source_id = $1 AND table_name = $2;`

// PostgresBackfillStore persists BackfillState to a shared Postgres table.
// It uses a single pgx.Conn (not a pool) — HA mode runs one instance per
// process so idle connection overhead from a pool provides no benefit.
type PostgresBackfillStore struct {
	conn *pgx.Conn
}

// OpenPostgresBackfillStore connects to Postgres at dsn and creates the
// kaptanto_backfill_states table if it does not exist.
// Uses pgx.Connect (not pgxpool) — single connection per instance.
func OpenPostgresBackfillStore(ctx context.Context, dsn string) (*PostgresBackfillStore, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("backfill: open postgres: %w", err)
	}

	if _, err := conn.Exec(ctx, createPostgresBackfillTableSQL); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("backfill: create schema: %w", err)
	}

	return &PostgresBackfillStore{conn: conn}, nil
}

// SaveState upserts a BackfillState row identified by (SourceID, Table).
// cursor_key is stored as BYTEA and survives binary round-trips.
func (s *PostgresBackfillStore) SaveState(ctx context.Context, state *BackfillState) error {
	var startedAt any
	if !state.StartedAt.IsZero() {
		startedAt = state.StartedAt.UTC()
	}

	_, err := s.conn.Exec(ctx, upsertPostgresBackfillStateSQL,
		state.SourceID,
		state.Table,
		state.Status,
		state.Strategy,
		state.CursorKey,
		state.TotalRows,
		state.ProcessedRows,
		int64(state.SnapshotLSN),
		startedAt,
	)
	if err != nil {
		return fmt.Errorf("backfill: save state (%s/%s): %w", state.SourceID, state.Table, err)
	}
	return nil
}

// LoadState returns the BackfillState for (sourceID, table), or (nil, nil) if
// no row exists (first run). This mirrors the SQLiteBackfillStore behaviour.
func (s *PostgresBackfillStore) LoadState(ctx context.Context, sourceID, table string) (*BackfillState, error) {
	var state BackfillState
	var startedAt, updatedAt *time.Time
	var snapshotLSN int64

	err := s.conn.QueryRow(ctx, selectPostgresBackfillStateSQL, sourceID, table).Scan(
		&state.SourceID,
		&state.Table,
		&state.Status,
		&state.Strategy,
		&state.CursorKey,
		&state.TotalRows,
		&state.ProcessedRows,
		&snapshotLSN,
		&startedAt,
		&updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backfill: load state (%s/%s): %w", sourceID, table, err)
	}

	state.SnapshotLSN = uint64(snapshotLSN)

	if startedAt != nil {
		state.StartedAt = startedAt.UTC()
	}
	if updatedAt != nil {
		state.UpdatedAt = updatedAt.UTC()
	}

	return &state, nil
}

// Close releases the pgx connection. Must be called on graceful shutdown.
func (s *PostgresBackfillStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.conn.Close(ctx); err != nil {
		return fmt.Errorf("backfill: close postgres store: %w", err)
	}
	return nil
}
