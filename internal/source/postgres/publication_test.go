package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRow is a minimal pgx.Row substitute. Scan copies count into dest[0]
// (an *int), or returns err if set.
type fakeRow struct {
	count int
	err   error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*int); ok {
			*p = r.count
		}
	}
	return nil
}

// fakePubConn implements pubQuerier without a live Postgres connection. It
// records the SQL passed to Exec so tests can assert on the generated
// CREATE PUBLICATION statement.
type fakePubConn struct {
	existCount   int
	existErr     error
	execErr      error
	execCalled   bool
	execSQL      string
	queryRowSQL  string
	queryRowArgs []any
}

func (f *fakePubConn) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryRowSQL = sql
	f.queryRowArgs = args
	return fakeRow{count: f.existCount, err: f.existErr}
}

func (f *fakePubConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execCalled = true
	f.execSQL = sql
	return pgconn.CommandTag{}, f.execErr
}

// TestEnsurePublication_AlreadyExists verifies that when the existence check
// returns count>0, ensurePublication returns nil without ever calling Exec.
func TestEnsurePublication_AlreadyExists(t *testing.T) {
	conn := &fakePubConn{existCount: 1}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{"public.orders"}, false)

	require.NoError(t, err)
	assert.False(t, conn.execCalled, "CREATE PUBLICATION must not run when the publication already exists")
}

// TestEnsurePublication_EmptyTablesAllowAllTablesFalse verifies the fail-closed
// guard: no tables configured and allowAllTables=false returns an error
// mentioning --all-tables, and never calls Exec.
func TestEnsurePublication_EmptyTablesAllowAllTablesFalse(t *testing.T) {
	conn := &fakePubConn{existCount: 0}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", nil, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all-tables")
	assert.False(t, conn.execCalled, "CREATE PUBLICATION must not run when the guard rejects the request")
}

// TestEnsurePublication_WithTables_CreatesForTable verifies that a non-empty
// tables slice produces "CREATE PUBLICATION ... FOR TABLE t1, t2" with the
// publication name sanitized as a quoted identifier.
func TestEnsurePublication_WithTables_CreatesForTable(t *testing.T) {
	conn := &fakePubConn{existCount: 0}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{"public.orders", "public.users"}, false)

	require.NoError(t, err)
	require.True(t, conn.execCalled, "Exec must be called to create the publication")
	assert.Contains(t, conn.execSQL, `CREATE PUBLICATION "kaptanto_pub" FOR TABLE`)
	assert.Contains(t, conn.execSQL, "public.orders")
	assert.Contains(t, conn.execSQL, "public.users")
}

// TestEnsurePublication_AllowAllTablesTrue_CreatesForAllTables verifies the
// FOR ALL TABLES path when tables is empty and allowAllTables=true.
func TestEnsurePublication_AllowAllTablesTrue_CreatesForAllTables(t *testing.T) {
	conn := &fakePubConn{existCount: 0}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", nil, true)

	require.NoError(t, err)
	require.True(t, conn.execCalled, "Exec must be called to create the publication")
	assert.Contains(t, conn.execSQL, `CREATE PUBLICATION "kaptanto_pub" FOR ALL TABLES`)
}

// TestEnsurePublication_ExistenceCheckError verifies that a QueryRow failure
// is wrapped and returned, without attempting Exec.
func TestEnsurePublication_ExistenceCheckError(t *testing.T) {
	conn := &fakePubConn{existErr: errors.New("connection reset")}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{"public.orders"}, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "check publication existence")
	assert.False(t, conn.execCalled)
}

// TestEnsurePublication_CreateError verifies that an Exec failure during
// CREATE PUBLICATION is wrapped and returned.
func TestEnsurePublication_CreateError(t *testing.T) {
	conn := &fakePubConn{existCount: 0, execErr: errors.New("permission denied")}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{"public.orders"}, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "create publication")
}
