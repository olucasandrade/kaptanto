package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// checkReplicaIdentity queries pg_class to find the REPLICA IDENTITY setting
// for the given schema.table. If the identity is 'd' (default — only PK
// columns are logged in old-row data), it emits a slog.Warn so operators
// know they may get incomplete before-images for UPDATE/DELETE events.
//
// This function never returns an error for a DEFAULT identity; it only logs.
func checkReplicaIdentity(ctx context.Context, conn *pgx.Conn, schema, table string) error {
	var relreplident string
	err := conn.QueryRow(ctx,
		`SELECT relreplident::text FROM pg_class
		 JOIN pg_namespace ON pg_namespace.oid = pg_class.relnamespace
		 WHERE pg_namespace.nspname = $1 AND pg_class.relname = $2`,
		schema, table,
	).Scan(&relreplident)
	if err != nil {
		// Table might not exist yet — log but don't fail.
		slog.Warn("postgres: could not check REPLICA IDENTITY",
			"schema", schema, "table", table, "error", err)
		return nil
	}

	if relreplident == "d" {
		slog.Warn("postgres: table uses default REPLICA IDENTITY — before-images for UPDATE/DELETE "+
			"will only include primary key columns; set REPLICA IDENTITY FULL for complete before-images",
			"schema", schema, "table", table)
	}
	return nil
}

// checkWALLag queries pg_stat_replication for the byte difference between
// sent_lsn and write_lsn on the primary. If the lag exceeds thresholdBytes,
// a slog.Warn is emitted. When m is non-nil, sets the SourceLagBytes gauge.
//
// Returns nil when:
//   - No standbys are attached (empty pg_stat_replication — not an error).
//   - Lag is within the threshold.
func checkWALLag(ctx context.Context, conn *pgx.Conn, thresholdBytes int64, sourceID string, m *observability.KaptantoMetrics) error {
	var lagBytes int64
	err := conn.QueryRow(ctx,
		`SELECT COALESCE(sent_lsn - write_lsn, 0) AS lag_bytes
		 FROM pg_stat_replication LIMIT 1`,
	).Scan(&lagBytes)
	if err != nil {
		// No rows — no standbys attached. This is normal for a single-node setup.
		return nil
	}

	if m != nil {
		m.SourceLagBytes.WithLabelValues(sourceID).Set(float64(lagBytes))
	}

	if lagBytes > thresholdBytes {
		slog.Warn("postgres: WAL lag exceeds threshold",
			"lag_bytes", lagBytes,
			"threshold_bytes", thresholdBytes,
		)
	}
	return nil
}

// splitSchemaTable splits a "schema.table" string into its two parts.
// If no dot is present, schema defaults to "public".
func splitSchemaTable(schemaTable string) (schema, table string) {
	for i := 0; i < len(schemaTable); i++ {
		if schemaTable[i] == '.' {
			return schemaTable[:i], schemaTable[i+1:]
		}
	}
	return "public", schemaTable
}

// checkPrimary runs SELECT pg_is_in_recovery() and returns an error if the
// target host is a standby. The replication connector must connect to a
// primary only (SRC-05).
func checkPrimary(ctx context.Context, conn *pgx.Conn) error {
	var inRecovery bool
	if err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		return fmt.Errorf("postgres: check primary status: %w", err)
	}
	if inRecovery {
		return fmt.Errorf("postgres: connected to a standby — replication requires a primary")
	}
	return nil
}
