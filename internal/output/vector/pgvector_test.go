package vector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePGX struct {
	execSQL  []string
	batchLen int
	execErr  error
	batchErr error
	pingErr  error
	closeErr error
	closed   bool
	// failExecOn makes the Nth Exec (1-based) return execErrNth.
	failExecOn int
	execN      int
	execErrNth error
}

func (f *fakePGX) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execN++
	f.execSQL = append(f.execSQL, sql)
	if f.failExecOn > 0 && f.execN == f.failExecOn {
		err := f.execErrNth
		if err == nil {
			err = errors.New("exec fail")
		}
		return pgconn.CommandTag{}, err
	}
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakePGX) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	f.batchLen = b.Len()
	return &fakeBatchResults{n: b.Len(), err: f.batchErr}
}

func (f *fakePGX) Ping(context.Context) error { return f.pingErr }

func (f *fakePGX) Close(context.Context) error {
	f.closed = true
	return f.closeErr
}

type fakeBatchResults struct {
	n, i int
	err  error
}

func (f *fakeBatchResults) Exec() (pgconn.CommandTag, error) {
	f.i++
	if f.err != nil {
		return pgconn.CommandTag{}, f.err
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (f *fakeBatchResults) Query() (pgx.Rows, error) { return nil, errors.New("unused") }
func (f *fakeBatchResults) QueryRow() pgx.Row        { return nil }
func (f *fakeBatchResults) Close() error             { return nil }

func TestPGVectorStore_UnitPaths(t *testing.T) {
	db := &fakePGX{}
	s := &PGVectorStore{conn: db, table: "kaptanto_vectors", dimensions: 3}

	require.NoError(t, s.ensureSchema(context.Background()))
	require.Len(t, db.execSQL, 2)
	assert.Contains(t, db.execSQL[0], "CREATE EXTENSION")
	assert.Contains(t, db.execSQL[1], "CREATE TABLE IF NOT EXISTS")
	assert.Contains(t, db.execSQL[1], "vector(3)")

	require.NoError(t, s.ensureSchema(context.Background())) // idempotent SQL

	recs := []Record{
		{ID: "a", Vector: []float32{0.1, 0.2, 0.3}, Text: "", Metadata: nil},
		{ID: "b", Vector: []float32{0.4, 0.5, 0.6}, Text: "hi", Metadata: map[string]any{"k": "v"}},
	}
	require.NoError(t, s.Upsert(context.Background(), recs))
	assert.Equal(t, 2, db.batchLen, "order preserved by construction: single batch")

	require.NoError(t, s.Upsert(context.Background(), nil))
	require.NoError(t, s.Delete(context.Background(), nil))

	require.NoError(t, s.Delete(context.Background(), []string{"a", "b"}))
	assert.True(t, strings.Contains(db.execSQL[len(db.execSQL)-1], "DELETE FROM"))

	require.NoError(t, s.Ping(context.Background()))
	require.NoError(t, s.Close())
	assert.True(t, db.closed)
	require.NoError(t, s.Close())
	require.NoError(t, (*PGVectorStore)(nil).Close())
}

func TestPGVectorStore_ErrorPaths(t *testing.T) {
	s := &PGVectorStore{conn: &fakePGX{execErr: errors.New("ext fail")}, table: "t", dimensions: 2}
	err := s.ensureSchema(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CREATE EXTENSION")

	// Concurrent CREATE EXTENSION IF NOT EXISTS can raise 23505; we treat it as
	// "extension already exists" and continue to CREATE TABLE.
	sConcurrent := &PGVectorStore{conn: &fakePGX{failExecOn: 1, execErrNth: &pgconn.PgError{Code: "23505"}}, table: "t", dimensions: 2}
	require.NoError(t, sConcurrent.ensureSchema(context.Background()))

	// Concurrent CREATE TABLE IF NOT EXISTS can also raise 23505 or 42P07; treat as success.
	sConcurrentTable := &PGVectorStore{conn: &fakePGX{failExecOn: 2, execErrNth: &pgconn.PgError{Code: "23505"}}, table: "t", dimensions: 2}
	require.NoError(t, sConcurrentTable.ensureSchema(context.Background()))

	sDupTable := &PGVectorStore{conn: &fakePGX{failExecOn: 2, execErrNth: &pgconn.PgError{Code: "42P07"}}, table: "t", dimensions: 2}
	require.NoError(t, sDupTable.ensureSchema(context.Background()))

	s2 := &PGVectorStore{conn: &fakePGX{failExecOn: 2, execErrNth: errors.New("table fail")}, table: "t", dimensions: 2}
	err = s2.ensureSchema(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CREATE TABLE")

	s3 := &PGVectorStore{conn: &fakePGX{batchErr: errors.New("batch boom")}, table: "t", dimensions: 2}
	err = s3.Upsert(context.Background(), []Record{{ID: "x", Vector: []float32{1, 2}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upsert[0]")

	s4 := &PGVectorStore{conn: &fakePGX{execErr: errors.New("del")}, table: "t", dimensions: 2}
	require.Error(t, s4.Delete(context.Background(), []string{"x"}))

	s5 := &PGVectorStore{conn: &fakePGX{pingErr: errors.New("pong")}, table: "t", dimensions: 2}
	require.Error(t, s5.Ping(context.Background()))

	s6 := &PGVectorStore{conn: &fakePGX{closeErr: errors.New("bye")}, table: "t", dimensions: 2}
	require.Error(t, s6.Close())
}

func TestNullIfEmpty(t *testing.T) {
	assert.Nil(t, nullIfEmpty(""))
	assert.Equal(t, "x", nullIfEmpty("x"))
}

func TestOpenPGVector_DefaultTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := OpenPGVector(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=1", "", 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect")
}
