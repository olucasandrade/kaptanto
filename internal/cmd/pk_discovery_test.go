package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRows is a minimal pgx.Rows fake driven by a canned set of single
// string-column rows (primary-key column names in index order), or a canned
// post-iteration error surfaced from Err().
type fakeRows struct {
	values []string
	idx    int
	err    error
}

var _ pgx.Rows = (*fakeRows)(nil)

func (f *fakeRows) Close()                                       {}
func (f *fakeRows) Err() error                                   { return f.err }
func (f *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                              { return nil }

func (f *fakeRows) Next() bool {
	if f.err != nil || f.idx >= len(f.values) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeRows) Scan(dest ...any) error {
	ptr, ok := dest[0].(*string)
	if !ok {
		return errors.New("fakeRows: unsupported scan destination")
	}
	*ptr = f.values[f.idx-1]
	return nil
}

// fakeQuerier is a pkQuerier fake keyed by the bound table-identity argument
// ($1), so each test controls exactly what each table "resolves" to without
// ever building real SQL or a real connection.
type fakeQuerier struct {
	byIdentity map[string]*fakeRows
	queryErr   map[string]error
	calls      []string
}

func (f *fakeQuerier) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	identity, _ := args[0].(string)
	f.calls = append(f.calls, identity)
	if err, ok := f.queryErr[identity]; ok {
		return nil, err
	}
	rows, ok := f.byIdentity[identity]
	if !ok {
		return &fakeRows{}, nil
	}
	// Return a fresh copy so repeated calls against the same fixture in one
	// test don't share iteration state (idx).
	cp := *rows
	return &cp, nil
}

func TestDiscoverPrimaryKey(t *testing.T) {
	t.Run("single-column PK", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.orders": {values: []string{"order_id"}},
		}}
		cols, err := discoverPrimaryKey(context.Background(), q, "public.orders")
		require.NoError(t, err)
		assert.Equal(t, []string{"order_id"}, cols)
	})

	t.Run("composite PK preserves index column order", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.order_items": {values: []string{"order_id", "line_no"}},
		}}
		cols, err := discoverPrimaryKey(context.Background(), q, "public.order_items")
		require.NoError(t, err)
		assert.Equal(t, []string{"order_id", "line_no"}, cols)
	})

	t.Run("no primary key returns empty slice, not error", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.no_pk_table": {values: nil},
		}}
		cols, err := discoverPrimaryKey(context.Background(), q, "public.no_pk_table")
		require.NoError(t, err)
		assert.Empty(t, cols)
	})

	t.Run("query error is wrapped with the table identity", func(t *testing.T) {
		q := &fakeQuerier{queryErr: map[string]error{
			"public.missing": errors.New(`relation "public.missing" does not exist`),
		}}
		_, err := discoverPrimaryKey(context.Background(), q, "public.missing")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public.missing")
	})

	t.Run("rows.Err after iteration is surfaced", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.flaky": {values: []string{"id"}, err: errors.New("connection reset")},
		}}
		_, err := discoverPrimaryKey(context.Background(), q, "public.flaky")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset")
	})

	t.Run("table identity is passed as a bind parameter, not interpolated", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			`public."weird; drop table x"`: {values: []string{"id"}},
		}}
		cols, err := discoverPrimaryKey(context.Background(), q, `public."weird; drop table x"`)
		require.NoError(t, err)
		assert.Equal(t, []string{"id"}, cols)
		require.Len(t, q.calls, 1)
		assert.Equal(t, `public."weird; drop table x"`, q.calls[0])
	})
}

func TestDiscoverPrimaryKeys(t *testing.T) {
	t.Run("resolves PK for every configured table", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.orders":      {values: []string{"order_id"}},
			"public.order_items": {values: []string{"order_id", "line_no"}},
		}}
		tables := map[string]config.TableConfig{
			"public.orders":      {},
			"public.order_items": {},
		}
		result, err := discoverPrimaryKeys(context.Background(), q, tables)
		require.NoError(t, err)
		assert.Equal(t, []string{"order_id"}, result["public.orders"])
		assert.Equal(t, []string{"order_id", "line_no"}, result["public.order_items"])
	})

	t.Run("fails fast for a table with no primary key", func(t *testing.T) {
		q := &fakeQuerier{byIdentity: map[string]*fakeRows{
			"public.orders":    {values: []string{"order_id"}},
			"public.audit_log": {values: nil},
		}}
		tables := map[string]config.TableConfig{
			"public.orders":    {},
			"public.audit_log": {},
		}
		_, err := discoverPrimaryKeys(context.Background(), q, tables)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public.audit_log")
		assert.Contains(t, err.Error(), "no primary key")
	})

	t.Run("empty tables map returns empty result and issues no queries", func(t *testing.T) {
		q := &fakeQuerier{}
		result, err := discoverPrimaryKeys(context.Background(), q, nil)
		require.NoError(t, err)
		assert.Empty(t, result)
		assert.Empty(t, q.calls)
	})

	t.Run("propagates the underlying query error for a bad table", func(t *testing.T) {
		q := &fakeQuerier{queryErr: map[string]error{
			"public.ghost": errors.New(`relation "public.ghost" does not exist`),
		}}
		tables := map[string]config.TableConfig{
			"public.ghost": {},
		}
		_, err := discoverPrimaryKeys(context.Background(), q, tables)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public.ghost")
	})
}
