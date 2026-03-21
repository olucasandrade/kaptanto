package loadgen

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

// RunBurst executes a burst scenario: ramp from 0 to 50k ops/s over 5s,
// hold at 50k for 10s, then drop to 10k ops/s for the remainder of ctx.
func RunBurst(ctx context.Context, pool *pgxpool.Pool, batchSize int, payloadBytes int) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	payload := GeneratePayload(payloadBytes)

	// Start at zero rate; burst=50000 prevents WaitN errors at high batch sizes.
	lim := rate.NewLimiter(0, 50000)

	// Ramp phase: 10 steps x 500ms = 5s, incrementing by 5000 ops/s each step.
	for step := 1; step <= 10; step++ {
		lim.SetLimit(rate.Limit(step * 5000))
		lim.SetBurst(50000)

		// Insert during this ramp step.
		if err := insertBatch(ctx, conn, lim, batchSize, payload); err != nil {
			return nil // context done
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}

	// Hold phase: 50k ops/s for 10s.
	lim.SetLimit(rate.Limit(50000))
	lim.SetBurst(50000)
	holdDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(holdDeadline) {
		if err := insertBatch(ctx, conn, lim, batchSize, payload); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}

	// Tail phase: 10k ops/s until ctx expires.
	lim.SetLimit(rate.Limit(10000))
	for {
		if err := insertBatch(ctx, conn, lim, batchSize, payload); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

// insertBatch reserves batchSize tokens from lim and copies one batch of rows.
func insertBatch(ctx context.Context, conn *pgxpool.Conn, lim *rate.Limiter, batchSize int, payload string) error {
	if err := lim.WaitN(ctx, batchSize); err != nil {
		return err
	}

	rows := make([][]any, batchSize)
	for i := range rows {
		rows[i] = []any{GenerateID(), payload, time.Now().UTC()}
	}

	_, err := conn.CopyFrom(
		ctx,
		pgx.Identifier{"bench_events"},
		[]string{"id", "payload", "_bench_ts"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		log.Printf("burst: insert error: %v", err)
	}
	return nil
}
