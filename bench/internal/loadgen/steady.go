package loadgen

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

// RunSteady inserts rows into bench_events at a rate controlled by lim.
// It acquires one connection and loops until ctx is done.
func RunSteady(ctx context.Context, pool *pgxpool.Pool, lim *rate.Limiter, batchSize int, payloadBytes int) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	payload := GeneratePayload(payloadBytes)

	for {
		if err := lim.WaitN(ctx, batchSize); err != nil {
			// Context cancelled or deadline exceeded — not an error.
			return nil
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
			log.Printf("steady: insert error: %v", err)
		}
	}
}
