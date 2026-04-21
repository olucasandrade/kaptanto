// Command statsd polls Docker container CPU% and VmRSS memory every --interval
// and appends one JSON line per container per tick to --output (NDJSON).
//
// Usage:
//
//	statsd --containers=kaptanto,debezium,sequin,peerdb --output=docker_stats.jsonl --interval=2s
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/statsd"
)

func main() {
	containers := flag.String("containers", "kaptanto,debezium,sequin,peerdb",
		"Comma-separated list of container names to poll")
	output := flag.String("output", "docker_stats.jsonl",
		"Path to NDJSON output file (appended on each run)")
	interval := flag.Duration("interval", 2*time.Second,
		"Polling interval (e.g. 2s, 500ms)")
	flag.Parse()

	names := strings.Split(*containers, ",")
	// Strip accidental whitespace around names.
	for i, n := range names {
		names[i] = strings.TrimSpace(n)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("statsd: polling %v every %s -> %s", names, *interval, *output)

	if err := statsd.RunPoller(ctx, names, *output, *interval); err != nil {
		log.Fatalf("statsd: %v", err)
	}

	log.Printf("statsd: done")
}
