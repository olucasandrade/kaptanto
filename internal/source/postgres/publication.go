// Package postgres implements the Postgres Change Data Capture source connector.
// It connects via logical replication (pglogrepl) and a separate query connection
// (pgx), manages slots and publications, and emits *event.ChangeEvent values.
package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olucasandrade/kaptanto/internal/pgident"
)

// pubQuerier is the minimal surface ensurePublication needs from a Postgres
// connection: an existence check (QueryRow) and a CREATE PUBLICATION (Exec).
// *pgx.Conn satisfies this interface, so production callers pass it unchanged;
// tests can substitute a fake to exercise the guard without a live database.
type pubQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ensurePublication checks whether a publication named pubName exists in
// pg_publication. If it does not, it creates one.
//
// When tables is non-empty, only those tables are included:
//
//	CREATE PUBLICATION pubName FOR TABLE t1, t2, ...
//
// When tables is empty and allowAllTables is true, the publication covers the
// entire database:
//
//	CREATE PUBLICATION pubName FOR ALL TABLES
//
// When tables is empty and allowAllTables is false, an error is returned.
// The startup guard in cmd/root.go should have already rejected this case;
// the check here is a defence-in-depth backstop.
func ensurePublication(ctx context.Context, conn pubQuerier, pubName string, tables []string, allowAllTables bool) error {
	var count int
	err := conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_publication WHERE pubname = $1", pubName,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("postgres: check publication existence: %w", err)
	}
	if count > 0 {
		return verifyPublicationMembership(ctx, conn, pubName, tables, allowAllTables)
	}

	var createSQL string
	if len(tables) == 0 {
		if !allowAllTables {
			return fmt.Errorf("postgres: cannot create publication %q: no tables specified and --all-tables opt-in is not set", pubName)
		}
		createSQL = fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", pgx.Identifier{pubName}.Sanitize())
	} else {
		qualified := make([]string, 0, len(tables))
		for _, t := range tables {
			schema, table, parseErr := pgident.Parse(t)
			if parseErr != nil {
				return fmt.Errorf("postgres: invalid table %q for publication: %w", t, parseErr)
			}
			if schema == "" {
				schema = "public"
			}
			qualified = append(qualified, pgident.Qualify(schema, table))
		}
		createSQL = fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s",
			pgx.Identifier{pubName}.Sanitize(),
			strings.Join(qualified, ", "),
		)
	}

	if _, err := conn.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("postgres: create publication %q: %w", pubName, err)
	}
	return nil
}

// verifyPublicationMembership fails startup when an existing publication's
// FOR ALL TABLES flag or table list does not match this process's config.
func verifyPublicationMembership(ctx context.Context, conn pubQuerier, pubName string, tables []string, allowAllTables bool) error {
	var pubAll bool
	var members []string
	err := conn.QueryRow(ctx, `
SELECT p.puballtables,
       COALESCE((
         SELECT array_agg(schemaname || '.' || tablename ORDER BY schemaname, tablename)
         FROM pg_publication_tables
         WHERE pubname = $1
       ), '{}'::text[])
FROM pg_publication p
WHERE p.pubname = $1`, pubName).Scan(&pubAll, &members)
	if err != nil {
		return fmt.Errorf("postgres: inspect publication %q membership: %w", pubName, err)
	}
	if allowAllTables {
		if !pubAll {
			return fmt.Errorf("postgres: publication %q exists but is not FOR ALL TABLES (refusing to reuse a narrower publication)", pubName)
		}
		return nil
	}
	if pubAll {
		return fmt.Errorf("postgres: publication %q is FOR ALL TABLES but config lists specific tables (refusing to capture extra relations)", pubName)
	}
	want, err := canonicalTableSet(tables)
	if err != nil {
		return err
	}
	have := make(map[string]struct{}, len(members))
	for _, m := range members {
		have[m] = struct{}{}
	}
	if len(have) != len(want) {
		return fmt.Errorf("postgres: publication %q table list does not match configured tables", pubName)
	}
	for t := range want {
		if _, ok := have[t]; !ok {
			return fmt.Errorf("postgres: publication %q table list does not match configured tables", pubName)
		}
	}
	return nil
}

func canonicalTableSet(tables []string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(tables))
	for _, t := range tables {
		schema, table, err := pgident.Parse(t)
		if err != nil {
			return nil, fmt.Errorf("postgres: invalid table %q for publication: %w", t, err)
		}
		if schema == "" {
			schema = "public"
		}
		out[schema+"."+table] = struct{}{}
	}
	return out, nil
}

// ensureSlot checks whether the replication slot named slotName exists.
// If it does not exist:
//   - wasEverConnected=true  → the slot was lost (e.g. failover); sets needsSnapshot=true (SRC-06)
//   - wasEverConnected=false → first run; creates slot, no snapshot needed
//
// On creation, ConsistentPoint is returned for use as a backfill snapshot
// coordinate in Phase 4.
//
// replConn is the replication *pgconn.PgConn (pglogrepl requires this).
// queryConn is used to check pg_replication_slots via SQL.
func ensureSlot(
	ctx context.Context,
	replConn *pgconn.PgConn,
	queryConn *pgx.Conn,
	slotName string,
	wasEverConnected bool,
) (needsSnapshot bool, consistentPoint pglogrepl.LSN, err error) {
	var count int
	if err = queryConn.QueryRow(ctx,
		"SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1", slotName,
	).Scan(&count); err != nil {
		return false, 0, fmt.Errorf("postgres: check slot existence: %w", err)
	}

	if count > 0 {
		return false, 0, nil // slot present, no snapshot needed
	}

	// Slot is absent.
	if wasEverConnected {
		needsSnapshot = true
	}

	// Create the slot on the replication connection (pglogrepl requirement).
	result, createErr := pglogrepl.CreateReplicationSlot(ctx, replConn, slotName, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{Temporary: false})
	if createErr != nil {
		return needsSnapshot, 0, fmt.Errorf("postgres: create replication slot %q: %w", slotName, createErr)
	}

	cp, parseErr := pglogrepl.ParseLSN(result.ConsistentPoint)
	if parseErr != nil {
		// Non-fatal: return zero LSN if parsing fails.
		return needsSnapshot, 0, nil
	}
	return needsSnapshot, cp, nil
}
