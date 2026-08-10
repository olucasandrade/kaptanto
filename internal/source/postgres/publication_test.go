package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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
	assert.Contains(t, conn.queryRowSQL, "pg_publication", "existence check must query pg_publication")
	assert.Equal(t, []any{"kaptanto_pub"}, conn.queryRowArgs, "existence check must filter by the given publication name")
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
// publication name and table identifiers sanitized as quoted identifiers.
func TestEnsurePublication_WithTables_CreatesForTable(t *testing.T) {
	conn := &fakePubConn{existCount: 0}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{"public.orders", "public.users"}, false)

	require.NoError(t, err)
	require.True(t, conn.execCalled, "Exec must be called to create the publication")
	assert.Contains(t, conn.execSQL, `CREATE PUBLICATION "kaptanto_pub" FOR TABLE`)
	assert.Contains(t, conn.execSQL, `"public"."orders"`)
	assert.Contains(t, conn.execSQL, `"public"."users"`)
}

// TestEnsurePublication_QuotedMixedCaseTable verifies issue #56: a config key
// like public."CamelCaseTable" must produce a single-quoted identifier pair,
// never the triple-quoted "public"."""CamelCaseTable""".
func TestEnsurePublication_QuotedMixedCaseTable(t *testing.T) {
	conn := &fakePubConn{existCount: 0}

	err := ensurePublication(context.Background(), conn, "kaptanto_pub", []string{`public."CamelCaseTable"`}, false)

	require.NoError(t, err)
	require.True(t, conn.execCalled)
	assert.Contains(t, conn.execSQL, `"public"."CamelCaseTable"`)
	assert.NotContains(t, conn.execSQL, `"""`)
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

// TestEnsurePublication_Integration_AllowAllTablesTrue is an integration test
// that requires a live Postgres instance. It creates a publication with
// allowAllTables=true and asserts pg_publication.puballtables is true — the
// one code path (CREATE PUBLICATION ... FOR ALL TABLES) that cannot be
// exercised without a real connection. Set POSTGRES_TEST_DSN to enable it,
// matching the gate used throughout this repo (see internal/ha/leader_test.go,
// internal/checkpoint/postgres_test.go). *pgx.Conn satisfies pubQuerier
// structurally, so it can be passed directly.
func TestEnsurePublication_Integration_AllowAllTablesTrue(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run publication integration tests")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to Postgres")
	defer func() { _ = conn.Close(context.Background()) }()

	pubName := fmt.Sprintf("kaptanto_test_pub_alltables_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(),
			fmt.Sprintf("DROP PUBLICATION IF EXISTS %s", pgx.Identifier{pubName}.Sanitize()))
	})

	require.NoError(t, ensurePublication(ctx, conn, pubName, nil, true),
		"ensurePublication with allowAllTables=true")

	var allTables bool
	err = conn.QueryRow(ctx,
		"SELECT puballtables FROM pg_publication WHERE pubname = $1", pubName,
	).Scan(&allTables)
	require.NoError(t, err, "query pg_publication")
	assert.True(t, allTables, "expected pg_publication.puballtables=true for a publication created with allowAllTables=true")

	// Re-running ensurePublication against the now-existing publication must
	// be a no-op (the existence check short-circuits before CREATE).
	require.NoError(t, ensurePublication(ctx, conn, pubName, nil, true),
		"ensurePublication should be idempotent when the publication already exists")
}
