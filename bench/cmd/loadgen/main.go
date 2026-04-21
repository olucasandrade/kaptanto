package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/olucasandrade/kaptanto/bench/internal/loadgen"
	"golang.org/x/time/rate"
)

const defaultDSN = "postgres://bench:bench@localhost:5432/bench"

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN; falls back to env BENCH_DSN")
	rateFlag := flag.Int("rate", 10000, "Target rows/sec")
	mode := flag.String("mode", "steady", "steady|burst|large-batch|idle")
	duration := flag.Duration("duration", 30*time.Second, "How long to run")
	batchSize := flag.Int("batch-size", 500, "Rows per CopyFrom batch")
	payloadKB := flag.Int("payload-kb", 1, "Approximate payload size in KB per row")
	flag.Parse()

	// Resolve DSN: flag > env > default.
	resolvedDSN := *dsn
	if resolvedDSN == "" {
		resolvedDSN = os.Getenv("BENCH_DSN")
	}
	if resolvedDSN == "" {
		resolvedDSN = defaultDSN
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, resolvedDSN)
	if err != nil {
		log.Fatalf("loadgen: create pool: %v", err)
	}
	defer pool.Close()

	if err := loadgen.EnsureSchema(ctx, pool); err != nil {
		log.Fatalf("loadgen: ensure schema: %v", err)
	}

	// burst=50000 prevents WaitN from returning an error at high batch sizes.
	lim := rate.NewLimiter(rate.Limit(*rateFlag), 50000)

	runCtx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	payloadBytes := *payloadKB * 1024

	start := time.Now()

	switch *mode {
	case "steady":
		err = loadgen.RunSteady(runCtx, pool, lim, *batchSize, payloadBytes)
	case "burst":
		err = loadgen.RunBurst(runCtx, pool, *batchSize, payloadBytes)
	case "large-batch":
		err = loadgen.RunLargeBatch(runCtx, pool, payloadBytes)
	case "idle":
		err = loadgen.RunIdle(runCtx, pool)
	default:
		log.Fatalf("unknown mode: %s", *mode)
	}

	if err != nil {
		log.Printf("loadgen: %v", err)
	}

	fmt.Fprintf(os.Stderr, "loadgen: done in %s\n", time.Since(start).Round(time.Millisecond))
}
