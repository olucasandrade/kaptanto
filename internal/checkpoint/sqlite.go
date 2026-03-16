package checkpoint

import (
	"context"
	"database/sql"
	"fmt"

	// Register the "sqlite" driver provided by modernc.org/sqlite.
	// This is a pure-Go SQLite implementation — CGO_ENABLED=0 builds succeed.
	_ "modernc.org/sqlite"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS source_checkpoints (
    source_id  TEXT PRIMARY KEY,
    lsn        TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`

const upsertSQL = `
INSERT INTO source_checkpoints (source_id, lsn)
VALUES (?, ?)
ON CONFLICT(source_id) DO UPDATE SET lsn = excluded.lsn, updated_at = CURRENT_TIMESTAMP;`

const selectSQL = `SELECT lsn FROM source_checkpoints WHERE source_id = ?;`

// SQLiteStore is a CheckpointStore backed by an on-disk SQLite database in
// WAL mode. It requires no CGO — it uses the pure-Go modernc.org/sqlite driver.
type SQLiteStore struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and initialises the
// schema. The database is configured with WAL journal mode and NORMAL
// synchronous mode for a balance of durability and performance.
//
// path must be a file-system path. Use t.TempDir() in tests.
func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open sqlite db: %w", err)
	}

	// Apply pragmas explicitly — encoding them in the DSN URI is unreliable
	// with modernc.org/sqlite and can trigger "out of memory" errors.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("checkpoint: apply pragma %q: %w", pragma, err)
		}
	}

	// Verify the connection and initialise schema.
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

// initSchema creates the source_checkpoints table if it does not already exist.
func initSchema(db *sql.DB) error {
	if _, err := db.Exec(createTableSQL); err != nil {
		return fmt.Errorf("checkpoint: create schema: %w", err)
	}
	return nil
}

// Save upserts sourceID → lsn. Calling Save twice for the same sourceID
// updates the existing row — it never creates duplicates.
func (s *SQLiteStore) Save(ctx context.Context, sourceID, lsn string) error {
	if _, err := s.db.ExecContext(ctx, upsertSQL, sourceID, lsn); err != nil {
		return fmt.Errorf("checkpoint: save %q=%q: %w", sourceID, lsn, err)
	}
	return nil
}

// Load returns the stored LSN for sourceID. If no checkpoint exists for
// sourceID (first run), it returns ("", nil) — not an error.
func (s *SQLiteStore) Load(ctx context.Context, sourceID string) (string, error) {
	var lsn string
	err := s.db.QueryRowContext(ctx, selectSQL, sourceID).Scan(&lsn)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("checkpoint: load %q: %w", sourceID, err)
	}
	return lsn, nil
}

// Close calls db.Close which checkpoints the WAL and releases the file handle.
// It must be called on graceful shutdown.
func (s *SQLiteStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	return nil
}

// Ping checks SQLite connectivity using the standard database/sql health check.
func (s *SQLiteStore) Ping() error {
	return s.db.PingContext(context.Background())
}
