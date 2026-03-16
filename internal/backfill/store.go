// Package backfill provides the Backfill Engine for kaptanto: keyset cursor
// pagination, watermark deduplication, crash-resumable SQLite state, adaptive
// batch sizing, and all five snapshot strategies.
package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// BackfillState captures the persistent state for a single table backfill.
type BackfillState struct {
	SourceID      string
	Table         string
	Status        string // "pending"|"running"|"completed"|"failed"|"deferred"
	Strategy      string
	CursorKey     []byte // JSON bytes of last PK processed
	TotalRows     int64
	ProcessedRows int64
	SnapshotLSN   uint64
	StartedAt     time.Time
	UpdatedAt     time.Time
}

// BackfillStore is the persistence interface for BackfillState.
type BackfillStore interface {
	SaveState(ctx context.Context, state *BackfillState) error
	LoadState(ctx context.Context, sourceID, table string) (*BackfillState, error)
	Close() error
}

const createBackfillTableSQL = `
CREATE TABLE IF NOT EXISTS backfill_states (
    source_id       TEXT NOT NULL,
    table_name      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    strategy        TEXT NOT NULL,
    cursor_key      BLOB,
    cursor_sort     TEXT,
    total_rows      INTEGER DEFAULT 0,
    processed_rows  INTEGER DEFAULT 0,
    snapshot_lsn    INTEGER DEFAULT 0,
    started_at      DATETIME,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_id, table_name)
);`

const upsertBackfillStateSQL = `
INSERT INTO backfill_states (source_id, table_name, status, strategy, cursor_key, total_rows, processed_rows, snapshot_lsn, started_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(source_id, table_name) DO UPDATE SET
    status         = excluded.status,
    strategy       = excluded.strategy,
    cursor_key     = excluded.cursor_key,
    total_rows     = excluded.total_rows,
    processed_rows = excluded.processed_rows,
    snapshot_lsn   = excluded.snapshot_lsn,
    started_at     = excluded.started_at,
    updated_at     = CURRENT_TIMESTAMP;`

const selectBackfillStateSQL = `
SELECT source_id, table_name, status, strategy, cursor_key, total_rows, processed_rows, snapshot_lsn, started_at, updated_at
FROM backfill_states
WHERE source_id = ? AND table_name = ?;`

// SQLiteBackfillStore persists BackfillState to a SQLite database.
type SQLiteBackfillStore struct {
	db *sql.DB
}

// OpenSQLiteBackfillStore opens (or creates) the SQLite database at path and
// initialises the backfill_states schema. Uses WAL journal mode and NORMAL
// synchronous mode. Pure Go — CGO_ENABLED=0 compatible.
func OpenSQLiteBackfillStore(path string) (*SQLiteBackfillStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("backfill: open sqlite db: %w", err)
	}

	// Apply pragmas explicitly — encoding them in the DSN URI is unreliable
	// with modernc.org/sqlite and can trigger "out of memory" errors.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("backfill: apply pragma %q: %w", pragma, err)
		}
	}

	if _, err := db.Exec(createBackfillTableSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill: create schema: %w", err)
	}

	return &SQLiteBackfillStore{db: db}, nil
}

// SaveState upserts a BackfillState row identified by (SourceID, Table).
func (s *SQLiteBackfillStore) SaveState(ctx context.Context, state *BackfillState) error {
	var startedAt any
	if !state.StartedAt.IsZero() {
		startedAt = state.StartedAt.UTC().Format(time.RFC3339)
	}

	_, err := s.db.ExecContext(ctx, upsertBackfillStateSQL,
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

// LoadState returns the BackfillState for (sourceID, table), or nil if not found (first run).
func (s *SQLiteBackfillStore) LoadState(ctx context.Context, sourceID, table string) (*BackfillState, error) {
	row := s.db.QueryRowContext(ctx, selectBackfillStateSQL, sourceID, table)

	var state BackfillState
	var startedAt, updatedAt sql.NullString
	var snapshotLSN int64

	err := row.Scan(
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
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backfill: load state (%s/%s): %w", sourceID, table, err)
	}

	state.SnapshotLSN = uint64(snapshotLSN)

	if startedAt.Valid && startedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		state.StartedAt = t
	}
	if updatedAt.Valid && updatedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, updatedAt.String)
		state.UpdatedAt = t
	}

	return &state, nil
}

// Close closes the underlying database.
func (s *SQLiteBackfillStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("backfill: close store: %w", err)
	}
	return nil
}
