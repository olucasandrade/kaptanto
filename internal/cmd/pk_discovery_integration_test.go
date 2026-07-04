package cmd

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/config"
)

// TestDiscoverPrimaryKeys_Integration exercises discoverPrimaryKey and
// discoverPrimaryKeys against a live Postgres instance, covering the three
// cases the hardcoded []string{"id"} bug got wrong: a table whose primary
// key isn't named "id", a composite-PK table, and a table with no primary
// key at all.
//
// Set POSTGRES_TEST_DSN to a valid Postgres DSN to run these tests, e.g.:
//
//	POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/testdb" go test ./internal/cmd/... -run TestDiscoverPrimaryKeys_Integration
func TestDiscoverPrimaryKeys_Integration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}

	ctx := context.Background()
	conn := connectWithPingRetry(t, ctx, dsn)
	defer func() { _ = conn.Close(ctx) }()

	const (
		ordersTable   = "pk_discovery_orders"
		orderItemsTbl = "pk_discovery_order_items"
		auditLogTable = "pk_discovery_audit_log"
	)

	dropAll := func() {
		for _, tbl := range []string{ordersTable, orderItemsTbl, auditLogTable} {
			if _, err := conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+tbl); err != nil {
				t.Fatalf("drop table %s: %v", tbl, err)
			}
		}
	}

	dropAll()
	t.Cleanup(dropAll)

	setup := []string{
		// Primary key named order_id, not "id" — the case that previously
		// made the snapshot query fail outright (`column "id" does not exist`).
		`CREATE TABLE ` + ordersTable + ` (order_id bigint PRIMARY KEY, total numeric)`,
		// Composite primary key — the case that previously broke BKF-02
		// watermark suppression because the watermark pkMap could never
		// byte-match a two-column WAL key against a single hardcoded "id".
		`CREATE TABLE ` + orderItemsTbl + ` (order_id bigint, line_no int, qty int, PRIMARY KEY (order_id, line_no))`,
		// No primary key at all — must fail fast, not silently fall back.
		`CREATE TABLE ` + auditLogTable + ` (event_id bigint, message text)`,
	}
	for _, stmt := range setup {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	t.Run("non-id primary key is discovered", func(t *testing.T) {
		cols, err := discoverPrimaryKey(ctx, conn, ordersTable)
		if err != nil {
			t.Fatalf("discoverPrimaryKey: %v", err)
		}
		if len(cols) != 1 || cols[0] != "order_id" {
			t.Fatalf("got %v, want [order_id]", cols)
		}
	})

	t.Run("composite primary key preserves index column order", func(t *testing.T) {
		cols, err := discoverPrimaryKey(ctx, conn, orderItemsTbl)
		if err != nil {
			t.Fatalf("discoverPrimaryKey: %v", err)
		}
		if len(cols) != 2 || cols[0] != "order_id" || cols[1] != "line_no" {
			t.Fatalf("got %v, want [order_id line_no]", cols)
		}
	})

	t.Run("table with no primary key resolves to an empty slice, not an error", func(t *testing.T) {
		cols, err := discoverPrimaryKey(ctx, conn, auditLogTable)
		if err != nil {
			t.Fatalf("discoverPrimaryKey: %v", err)
		}
		if len(cols) != 0 {
			t.Fatalf("got %v, want empty", cols)
		}
	})

	t.Run("discoverPrimaryKeys fails fast when any configured table has no primary key", func(t *testing.T) {
		tables := map[string]config.TableConfig{
			ordersTable:   {},
			orderItemsTbl: {},
			auditLogTable: {},
		}
		_, err := discoverPrimaryKeys(ctx, conn, tables)
		if err == nil {
			t.Fatal("expected an error because of the PK-less table, got nil")
		}
		if !strings.Contains(err.Error(), auditLogTable) {
			t.Fatalf("error should mention %s, got: %v", auditLogTable, err)
		}
	})

	t.Run("discoverPrimaryKeys succeeds when every table has a primary key", func(t *testing.T) {
		tables := map[string]config.TableConfig{
			ordersTable:   {},
			orderItemsTbl: {},
		}
		result, err := discoverPrimaryKeys(ctx, conn, tables)
		if err != nil {
			t.Fatalf("discoverPrimaryKeys: %v", err)
		}
		if len(result[ordersTable]) != 1 || result[ordersTable][0] != "order_id" {
			t.Fatalf("orders PK = %v, want [order_id]", result[ordersTable])
		}
		if len(result[orderItemsTbl]) != 2 || result[orderItemsTbl][0] != "order_id" || result[orderItemsTbl][1] != "line_no" {
			t.Fatalf("order_items PK = %v, want [order_id line_no]", result[orderItemsTbl])
		}
	})
}

// connectWithPingRetry connects to Postgres and verifies the connection with
// a Ping, retrying the connect+ping pair a few times on failure. This CI
// environment's freshly-started Postgres container has, on this test, been
// observed to accept a connection successfully (pgx.Connect returns no
// error) that is then immediately unusable (`conn closed` on the first
// query) even though the server's own log shows nothing wrong — a transient
// condition in the container/network handshake, not the database itself.
// This mirrors the readiness-retry pattern already used for RabbitMQ/MongoDB
// startup elsewhere in this repo's CI workflows.
func connectWithPingRetry(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if pingErr := conn.Ping(ctx); pingErr != nil {
			lastErr = pingErr
			_ = conn.Close(ctx)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		return conn
	}
	t.Fatalf("connect (with ping) after %d attempts: %v", maxAttempts, lastErr)
	return nil
}
