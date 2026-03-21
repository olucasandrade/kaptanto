package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kaptanto/kaptanto/bench/internal/scenarios"
)

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN; falls back to BENCH_DSN env var then default")
	outputDir := flag.String("output-dir", "./results", "Directory for metrics.jsonl and docker_stats.jsonl")
	scenariosFlag := flag.String("scenarios", "", "Comma-separated scenario names to run; empty means all 5")
	collectorBin := flag.String("collector-bin", "./cmd/collector/collector", "Path to compiled collector binary")
	loadgenBin := flag.String("loadgen-bin", "./cmd/loadgen/loadgen", "Path to compiled loadgen binary")
	collectorURL := flag.String("collector-url", "http://localhost:8080", "Base URL for collector management API")
	statsdBin := flag.String("statsd-bin", "", "Path to compiled statsd binary (optional)")
	flag.Parse()

	// Resolve DSN: flag → env → default.
	resolvedDSN := *dsn
	if resolvedDSN == "" {
		resolvedDSN = os.Getenv("BENCH_DSN")
	}
	if resolvedDSN == "" {
		resolvedDSN = "postgres://bench:bench@localhost:5432/bench"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("scenarios: mkdir %s: %v", *outputDir, err)
	}

	// Start collector subprocess.
	collectorArgs := []string{"--output", filepath.Join(*outputDir, "metrics.jsonl")}
	collectorCmd := exec.CommandContext(ctx, *collectorBin, collectorArgs...)
	collectorCmd.Stdout = os.Stdout
	collectorCmd.Stderr = os.Stderr
	if err := collectorCmd.Start(); err != nil {
		log.Fatalf("scenarios: start collector: %v", err)
	}
	defer func() {
		if collectorCmd.Process != nil {
			_ = collectorCmd.Process.Signal(syscall.SIGTERM)
			_ = collectorCmd.Wait()
		}
	}()

	// Wait for collector HTTP to be ready (poll GET /scenario up to 5s).
	if err := waitForCollector(*collectorURL, 5*time.Second); err != nil {
		log.Printf("scenarios: collector not ready within 5s (continuing anyway): %v", err)
	}

	// Optionally start statsd subprocess.
	if *statsdBin != "" {
		statsdArgs := []string{"--output", filepath.Join(*outputDir, "docker_stats.jsonl")}
		statsdCmd := exec.CommandContext(ctx, *statsdBin, statsdArgs...)
		statsdCmd.Stdout = os.Stdout
		statsdCmd.Stderr = os.Stderr
		if err := statsdCmd.Start(); err != nil {
			log.Printf("scenarios: start statsd (ignored): %v", err)
		} else {
			defer func() {
				if statsdCmd.Process != nil {
					_ = statsdCmd.Process.Signal(syscall.SIGTERM)
					_ = statsdCmd.Wait()
				}
			}()
		}
	}

	runner := &scenarios.Runner{
		CollectorURL: *collectorURL,
		LoadgenBin:   *loadgenBin,
		DSN:          resolvedDSN,
		OutputDir:    *outputDir,
	}

	if err := runner.Init(ctx); err != nil {
		log.Printf("scenarios: init error (continuing): %v", err)
	}

	// Select scenarios to run.
	selected := scenarios.Scenarios
	if *scenariosFlag != "" {
		names := strings.Split(*scenariosFlag, ",")
		nameSet := make(map[string]bool, len(names))
		for _, n := range names {
			nameSet[strings.TrimSpace(n)] = true
		}
		filtered := make([]scenarios.ScenarioDef, 0, len(names))
		for _, s := range scenarios.Scenarios {
			if nameSet[s.Name] {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("scenarios: no matching scenarios found for %q", *scenariosFlag)
		}
		selected = filtered
	}

	if err := runner.Run(ctx, selected); err != nil {
		log.Printf("scenarios: run error: %v", err)
	}

	log.Printf("scenarios: complete — results in %s", *outputDir)
}

// waitForCollector polls GET /scenario on the collector management API until it
// responds with 200 or the deadline is exceeded.
func waitForCollector(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: time.Second}
	url := baseURL + "/scenario"
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("collector not ready at %s after %s", url, timeout)
}
