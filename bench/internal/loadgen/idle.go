package loadgen

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunIdle connects but performs no inserts. It sends a SELECT 1 heartbeat
// every 5 seconds and returns when ctx is done.
func RunIdle(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	log.Println("idle: connected, sending SELECT 1 heartbeats every 5s")

	for {
		_, err := conn.Exec(ctx, "SELECT 1")
		if err != nil {
			log.Printf("idle: heartbeat error: %v", err)
		} else {
			log.Println("idle: heartbeat OK")
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}
