package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Compile-time assertion: PostgresStore must satisfy CheckpointStore.
var _ CheckpointStore = (*PostgresStore)(nil)

const createPostgresTableSQL = `
CREATE TABLE IF NOT EXISTS postgres_checkpoints (
    source_id  TEXT PRIMARY KEY,
    lsn        TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);`

const upsertPostgresSQL = `
INSERT INTO postgres_checkpoints (source_id, lsn)
VALUES ($1, $2)
ON CONFLICT (source_id) DO UPDATE
    SET lsn        = EXCLUDED.lsn,
        updated_at = NOW();`

const selectPostgresSQL = `SELECT lsn FROM postgres_checkpoints WHERE source_id = $1;`

// PostgresStore is a CheckpointStore backed by a shared Postgres table.
// Both HA instances connect to the same DSN, so whichever instance is leader
// writes its LSN here and the standby reads it after takeover.
//
// Uses a single pgx.Conn (not a pool) — HA mode runs one instance per process
// so idle connection overhead from a pool provides no benefit.
type PostgresStore struct {
	conn *pgx.Conn
}

// OpenPostgres connects to Postgres at dsn and creates the postgres_checkpoints
// table if it does not exist. It returns a *PostgresStore ready for use.
//
// dsn must be a libpq-compatible connection string, e.g.:
//
//	"postgres://user:pass@localhost:5432/mydb"
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open postgres: %w", err)
	}

	if _, err := conn.Exec(ctx, createPostgresTableSQL); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("checkpoint: create schema: %w", err)
	}

	return &PostgresStore{conn: conn}, nil
}

// Save upserts sourceID → lsn into the postgres_checkpoints table.
// Calling Save twice with the same sourceID updates the existing row.
func (p *PostgresStore) Save(ctx context.Context, sourceID, lsn string) error {
	if _, err := p.conn.Exec(ctx, upsertPostgresSQL, sourceID, lsn); err != nil {
		return fmt.Errorf("checkpoint: save %q=%q: %w", sourceID, lsn, err)
	}
	return nil
}

// Load returns the stored LSN for sourceID. If no checkpoint exists for
// sourceID (first run), it returns ("", nil) — not an error.
func (p *PostgresStore) Load(ctx context.Context, sourceID string) (string, error) {
	var lsn string
	err := p.conn.QueryRow(ctx, selectPostgresSQL, sourceID).Scan(&lsn)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("checkpoint: load %q: %w", sourceID, err)
	}
	return lsn, nil
}

// Close releases the pgx connection. It must be called on graceful shutdown.
func (p *PostgresStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.conn.Close(ctx); err != nil {
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	return nil
}

// Ping checks Postgres connectivity with a short timeout.
// Used by the health handler to verify the connection is alive.
func (p *PostgresStore) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return p.conn.Ping(pingCtx)
}
