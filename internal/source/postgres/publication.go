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
)

// ensurePublication checks whether a publication named pubName exists in
// pg_publication. If it does not, it creates one for the given tables using:
//
//	CREATE PUBLICATION pubName FOR TABLE t1, t2, ...
//
// If tables is empty, the publication is created for ALL TABLES.
func ensurePublication(ctx context.Context, conn *pgx.Conn, pubName string, tables []string) error {
	var count int
	err := conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_publication WHERE pubname = $1", pubName,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("postgres: check publication existence: %w", err)
	}
	if count > 0 {
		return nil // already exists
	}

	var createSQL string
	if len(tables) == 0 {
		createSQL = fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", pgx.Identifier{pubName}.Sanitize())
	} else {
		createSQL = fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s",
			pgx.Identifier{pubName}.Sanitize(),
			strings.Join(tables, ", "),
		)
	}

	if _, err := conn.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("postgres: create publication %q: %w", pubName, err)
	}
	return nil
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
