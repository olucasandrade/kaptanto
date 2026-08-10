package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgxDB is the subset of *pgx.Conn used by PGVectorStore (testable).
type pgxDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Ping(ctx context.Context) error
	Close(ctx context.Context) error
}

// PGVectorStore persists vectors in a Postgres table with the pgvector extension.
type PGVectorStore struct {
	conn       pgxDB
	table      string // sanitized SQL identifier
	dimensions int
}

// OpenPGVector connects to dsn, ensures the table exists (CREATE TABLE IF NOT
// EXISTS — idempotent under concurrent cluster nodes), and returns a store.
// table defaults to DefaultPGVectorTable when empty.
func OpenPGVector(ctx context.Context, dsn, table string, dimensions int) (*PGVectorStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("vector: pgvector: dsn is required")
	}
	if dimensions <= 0 {
		return nil, fmt.Errorf("vector: pgvector: dimensions must be > 0")
	}
	table = strings.TrimSpace(table)
	if table == "" {
		table = DefaultPGVectorTable
	}
	if err := validateIdent(table); err != nil {
		return nil, fmt.Errorf("vector: pgvector: table: %w", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("vector: pgvector: connect: %w", err)
	}
	s := &PGVectorStore{conn: conn, table: table, dimensions: dimensions}
	if err := s.ensureSchema(ctx); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return s, nil
}

// ensureSchema creates the extension and table if missing. Safe to call
// concurrently from multiple nodes (CREATE … IF NOT EXISTS).
func (s *PGVectorStore) ensureSchema(ctx context.Context) error {
	if _, err := s.conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		// Concurrent CREATE EXTENSION IF NOT EXISTS can race on the unique
		// index pg_extension_name_index; a duplicate-key error means another
		// connection created the extension and we can proceed.
		if !isPGSchemaRace(err) {
			return fmt.Errorf("vector: pgvector: CREATE EXTENSION: %w", err)
		}
	}
	ident := pgx.Identifier{s.table}.Sanitize()
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id text PRIMARY KEY,
    "vector" vector(%d) NOT NULL,
    text text,
    metadata jsonb
)`, ident, s.dimensions)
	if _, err := s.conn.Exec(ctx, ddl); err != nil {
		// Concurrent CREATE TABLE IF NOT EXISTS can race: another connection may
		// create the table first (42P07 duplicate_table) or hit a unique index on
		// pg_type_typname_nsp_index (23505).
		if !isPGSchemaRace(err) {
			return fmt.Errorf("vector: pgvector: CREATE TABLE: %w", err)
		}
	}
	return nil
}

// isPGSchemaRace reports Postgres errors that mean another concurrent caller
// already created the extension or table we were about to create.
func isPGSchemaRace(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" || pgErr.Code == "42P07"
	}
	return false
}

// Upsert writes records in a single pgx.Batch (order preserved by construction).
func (s *PGVectorStore) Upsert(ctx context.Context, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	ident := pgx.Identifier{s.table}.Sanitize()
	sql := fmt.Sprintf(`
INSERT INTO %s (id, "vector", text, metadata)
VALUES ($1, $2::vector, $3, $4)
ON CONFLICT (id) DO UPDATE SET
    "vector" = EXCLUDED."vector",
    text = EXCLUDED.text,
    metadata = EXCLUDED.metadata`, ident)

	batch := &pgx.Batch{}
	for _, rec := range recs {
		meta, err := marshalMetadata(rec.Metadata)
		if err != nil {
			return fmt.Errorf("vector: pgvector: upsert metadata: %w", err)
		}
		batch.Queue(sql, rec.ID, formatVector(rec.Vector), nullIfEmpty(rec.Text), meta)
	}
	br := s.conn.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for i := 0; i < len(recs); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("vector: pgvector: upsert[%d]: %w", i, err)
		}
	}
	return nil
}

// Delete removes vectors by ID in a single statement (order of ids is irrelevant).
func (s *PGVectorStore) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ident := pgx.Identifier{s.table}.Sanitize()
	sql := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, ident)
	if _, err := s.conn.Exec(ctx, sql, ids); err != nil {
		return fmt.Errorf("vector: pgvector: delete: %w", err)
	}
	return nil
}

// Ping verifies the connection with SELECT 1.
func (s *PGVectorStore) Ping(ctx context.Context) error {
	if err := s.conn.Ping(ctx); err != nil {
		return fmt.Errorf("vector: pgvector: ping: %w", err)
	}
	return nil
}

// Close closes the underlying connection.
func (s *PGVectorStore) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	err := s.conn.Close(context.Background())
	s.conn = nil
	if err != nil {
		return fmt.Errorf("vector: pgvector: close: %w", err)
	}
	return nil
}

func formatVector(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.Grow(2 + len(v)*8)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}

func marshalMetadata(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// validateIdent allows only [A-Za-z_][A-Za-z0-9_]* table names.
func validateIdent(name string) error {
	if name == "" {
		return fmt.Errorf("empty identifier")
	}
	for i, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("invalid identifier %q", name)
		}
	}
	return nil
}
