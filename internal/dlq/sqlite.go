package dlq

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"

	// Register the "sqlite" driver provided by modernc.org/sqlite.
	// This is a pure-Go SQLite implementation — CGO_ENABLED=0 builds succeed.
	_ "modernc.org/sqlite"
)

const maxReasonBytes = 1024

// Package-level ULID generator — same monotonic source pattern as internal/event.
var idGen = event.NewIDGenerator()

const createTableSQL = `
CREATE TABLE IF NOT EXISTS dlq_events (
    id              TEXT PRIMARY KEY,
    consumer_id     TEXT    NOT NULL,
    event_id        TEXT    NOT NULL,
    table_name      TEXT    NOT NULL,
    partition_id    INTEGER NOT NULL,
    seq             INTEGER NOT NULL,
    attempts        INTEGER NOT NULL,
    reason          TEXT    NOT NULL,
    idempotency_key TEXT    NOT NULL,
    payload         BLOB    NOT NULL,
    created_at      INTEGER NOT NULL
);`

const createUniqueIndexSQL = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_dlq_consumer_event
    ON dlq_events(consumer_id, event_id);`

const createCreatedIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_dlq_consumer_created
    ON dlq_events(consumer_id, created_at);`

const insertSQL = `
INSERT INTO dlq_events (
    id, consumer_id, event_id, table_name, partition_id, seq,
    attempts, reason, idempotency_key, payload, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(consumer_id, event_id) DO NOTHING;`

const selectByIDSQL = `
SELECT id, consumer_id, event_id, table_name, partition_id, seq,
       attempts, reason, idempotency_key, payload, created_at
FROM dlq_events WHERE id = ?;`

// SQLiteStore is a Store backed by an on-disk SQLite database in WAL mode.
// It requires no CGO — it uses the pure-Go modernc.org/sqlite driver.
type SQLiteStore struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and initialises the
// schema. The database is configured with WAL journal mode, a 5s busy timeout,
// and NORMAL synchronous mode so a running pipeline and the CLI can share the
// file safely.
//
// path must be a file-system path. Use t.TempDir() in tests.
func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("dlq: open sqlite db: %w", err)
	}

	// Apply pragmas explicitly — encoding them in the DSN URI is unreliable
	// with modernc.org/sqlite and can trigger "out of memory" errors.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("dlq: apply pragma %q: %w", pragma, err)
		}
	}

	// Pin to one connection so connection-scoped pragmas (busy_timeout, WAL)
	// apply to every Exec/Query. Without this, database/sql may open a fresh
	// connection that never saw busy_timeout and returns SQLITE_BUSY immediately.
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

// initSchema creates the dlq_events table and indexes if they do not exist.
func initSchema(db *sql.DB) error {
	for _, stmt := range []string{createTableSQL, createUniqueIndexSQL, createCreatedIndexSQL} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("dlq: create schema: %w", err)
		}
	}
	return nil
}

// Write inserts e into the store. If e.ID is empty a ULID is minted. Reason is
// truncated to 1024 bytes. A conflicting (ConsumerID, EventID) is a no-op that
// returns nil (DLQ-02).
func (s *SQLiteStore) Write(ctx context.Context, e Entry) error {
	if e.ID == "" {
		e.ID = idGen.New().String()
	}
	e.Reason = truncateReason(e.Reason)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.Payload == nil {
		e.Payload = []byte{}
	}

	_, err := s.db.ExecContext(ctx, insertSQL,
		e.ID,
		e.ConsumerID,
		e.EventID,
		e.Table,
		int64(e.PartitionID),
		int64(e.Seq),
		e.Attempts,
		e.Reason,
		e.IdempotencyKey,
		e.Payload,
		e.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("dlq: write consumer=%q event=%q: %w", e.ConsumerID, e.EventID, err)
	}
	return nil
}

// List returns entries matching f, ordered by ConsumerID, PartitionID, Seq.
func (s *SQLiteStore) List(ctx context.Context, f Filter) ([]Entry, error) {
	query, args := buildFilterQuery(
		`SELECT id, consumer_id, event_id, table_name, partition_id, seq,
		        attempts, reason, idempotency_key, payload, created_at
		 FROM dlq_events`,
		f,
		true, // ORDER BY + LIMIT
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("dlq: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dlq: list rows: %w", err)
	}
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

// Get returns the entry with the given ID, or ErrNotFound.
func (s *SQLiteStore) Get(ctx context.Context, id string) (Entry, error) {
	row := s.db.QueryRowContext(ctx, selectByIDSQL, id)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("dlq: get %q: %w", id, err)
	}
	return e, nil
}

// Delete removes the entries with the given IDs. An empty ids list is a no-op.
func (s *SQLiteStore) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `DELETE FROM dlq_events WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("dlq: delete: %w", err)
	}
	return nil
}

// Purge deletes entries matching f and returns the number of rows removed.
// Limit is ignored for Purge — all matching rows are deleted.
func (s *SQLiteStore) Purge(ctx context.Context, f Filter) (int, error) {
	// Purge never applies Limit; clear it so buildFilterQuery omits LIMIT.
	f.Limit = 0
	query, args := buildFilterQuery(`DELETE FROM dlq_events`, f, false)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("dlq: purge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("dlq: purge rows affected: %w", err)
	}
	return int(n), nil
}

// Close checkpoints the WAL and releases the file handle.
func (s *SQLiteStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("dlq: close: %w", err)
	}
	return nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(sc rowScanner) (Entry, error) {
	var (
		e         Entry
		partition int64
		seq       int64
		createdMS int64
	)
	err := sc.Scan(
		&e.ID,
		&e.ConsumerID,
		&e.EventID,
		&e.Table,
		&partition,
		&seq,
		&e.Attempts,
		&e.Reason,
		&e.IdempotencyKey,
		&e.Payload,
		&createdMS,
	)
	if err != nil {
		return Entry{}, err
	}
	e.PartitionID = uint32(partition)
	e.Seq = uint64(seq)
	e.CreatedAt = time.UnixMilli(createdMS).UTC()
	return e, nil
}

// buildFilterQuery appends WHERE / ORDER BY / LIMIT clauses for f.
// When withOrder is true, results are ordered by consumer_id, partition_id, seq
// and Limit is applied when > 0.
func buildFilterQuery(base string, f Filter, withOrder bool) (string, []any) {
	var b strings.Builder
	b.WriteString(base)

	args := make([]any, 0, 4)
	where := make([]string, 0, 3)
	if f.ConsumerID != "" {
		where = append(where, "consumer_id = ?")
		args = append(args, f.ConsumerID)
	}
	if f.Table != "" {
		where = append(where, "table_name = ?")
		args = append(args, f.Table)
	}
	if !f.OlderThan.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, f.OlderThan.UTC().UnixMilli())
	}
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(where, " AND "))
	}
	if withOrder {
		b.WriteString(" ORDER BY consumer_id, partition_id, seq")
		if f.Limit > 0 {
			b.WriteString(" LIMIT ?")
			args = append(args, f.Limit)
		}
	}
	return b.String(), args
}

func truncateReason(s string) string {
	if len(s) <= maxReasonBytes {
		return s
	}
	return s[:maxReasonBytes]
}
