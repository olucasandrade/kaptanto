package cmd

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/config"
)

// pkDiscoveryQuery returns the ordered primary-key column names for a table.
// The table identity is bound as $1 (never string-interpolated into SQL) and
// resolved through Postgres's own ::regclass identifier resolution, so it
// honors schema qualification and search_path the same way the rest of
// Postgres does. array_position(i.indkey, a.attnum) preserves the index's
// declared column order, which matters for composite keys: keyset pagination
// and the WAL-derived watermark key must agree on column order.
const pkDiscoveryQuery = `
SELECT a.attname
FROM pg_index i
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
WHERE i.indrelid = $1::regclass AND i.indisprimary
ORDER BY array_position(i.indkey, a.attnum)`

// pkQuerier is the minimal slice of *pgx.Conn's API used for primary-key
// discovery. Its method signature matches *pgx.Conn.Query exactly, so
// *pgx.Conn satisfies it structurally with no adapter; unit tests use a fake.
type pkQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// discoverPrimaryKey queries the Postgres catalog for the ordered
// primary-key column names of tableIdentity (e.g. "public.orders" or,
// relying on search_path, "orders"). It returns a nil/empty slice — not an
// error — when the table has no primary key; callers decide how to treat
// that. See discoverPrimaryKeys for the fail-fast policy used by production
// wiring.
func discoverPrimaryKey(ctx context.Context, q pkQuerier, tableIdentity string) ([]string, error) {
	rows, err := q.Query(ctx, pkDiscoveryQuery, tableIdentity)
	if err != nil {
		return nil, fmt.Errorf("query primary key columns for %q: %w", tableIdentity, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("scan primary key column for %q: %w", tableIdentity, err)
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read primary key columns for %q: %w", tableIdentity, err)
	}
	return cols, nil
}

// discoverPrimaryKeys resolves the ordered primary-key columns for every
// table in tables (keyed the same way as config.Config.Tables, e.g.
// "public.orders" or "orders") against a live, non-replication Postgres
// connection.
//
// SRC-01: q must be a dedicated snapshot connection, never the replication
// connection — callers are expected to open and close a short-lived
// *pgx.Conn just for this call, separate from the connection the backfill
// engine's openConnFn later opens for the actual snapshot.
//
// Fails fast (returns an error, discovers nothing) for any table with no
// primary key: BKF-02's watermark suppression can never byte-match the
// WAL-derived key without one, so a table silently falling back to a guessed
// key would double-deliver or drop rows without any visible symptom. A
// clear startup error is safer than that.
func discoverPrimaryKeys(ctx context.Context, q pkQuerier, tables map[string]config.TableConfig) (map[string][]string, error) {
	result := make(map[string][]string, len(tables))
	for tableKey := range tables {
		cols, err := discoverPrimaryKey(ctx, q, tableKey)
		if err != nil {
			return nil, fmt.Errorf("discover primary key for table %q: %w", tableKey, err)
		}
		if len(cols) == 0 {
			return nil, fmt.Errorf(
				"table %q has no primary key: backfill requires one for keyset pagination and BKF-02 "+
					"watermark suppression — add a primary key to %q or remove it from the configured tables",
				tableKey, tableKey)
		}
		result[tableKey] = cols
	}
	return result, nil
}
