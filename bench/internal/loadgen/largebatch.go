package loadgen

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const largeBatchSize = 100_000

// RunLargeBatch inserts exactly 100,000 rows in a single transaction using
// one CopyFrom call. Timestamps are captured client-side at population time,
// advancing per-row as time.Now() is called in the loop.
func RunLargeBatch(ctx context.Context, pool *pgxpool.Pool, payloadBytes int) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	payload := GeneratePayload(payloadBytes)

	start := time.Now()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	rows := make([][]any, largeBatchSize)
	for i := range rows {
		rows[i] = []any{GenerateID(), payload, time.Now().UTC()}
	}

	n, err := tx.CopyFrom(
		ctx,
		pgx.Identifier{"bench_events"},
		[]string{"id", "payload", "_bench_ts"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("copy from: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	elapsed := time.Since(start)
	log.Printf("large-batch: inserted %d rows in %s (%.0f rows/s)", n, elapsed, float64(n)/elapsed.Seconds())

	return nil
}
