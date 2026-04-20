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
	statsdContainers := flag.String("statsd-containers", "", "Comma-separated container names for statsd (default: bench-kaptanto-1,bench-debezium-1,bench-sequin-1,bench-peerdb-server-1)")
	composeDir := flag.String("compose-dir", "", "Directory containing docker-compose.yml. When set, each scenario gets a fresh isolated stack (down -v → pre-conditions SQL → up -d)")
	drainWait := flag.Bool("drain-wait", false, "Wait for kaptanto WAL backlogs to drain before each scenario")
	drainIdleSecs := flag.Int("drain-idle-secs", 10, "Seconds of collector inactivity to declare a tool idle during --drain-wait")
	drainTimeoutSecs := flag.Int("drain-timeout", 300, "Max seconds to wait during --drain-wait before proceeding")
	flag.Parse()

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

	containers := *statsdContainers
	if containers == "" {
		containers = "bench-kaptanto-1,bench-debezium-1,bench-sequin-1,bench-peerdb-server-1"
	}

	if *composeDir != "" {
		runIsolated(ctx, selected, resolvedDSN, *outputDir, *collectorBin, *loadgenBin,
			*collectorURL, *statsdBin, containers, *composeDir,
			*drainWait, *drainIdleSecs, *drainTimeoutSecs)
	} else {
		runLegacy(ctx, selected, resolvedDSN, *outputDir, *collectorBin, *loadgenBin,
			*collectorURL, *statsdBin, containers,
			*drainWait, *drainIdleSecs, *drainTimeoutSecs)
	}

	log.Printf("scenarios: complete — results in %s", *outputDir)
}

// runIsolated executes each scenario against a freshly provisioned stack.
// For each scenario: down -v → infra up → pre-conditions SQL → full up → wait healthy
// → fresh collector+statsd → init → drain → single scenario → stop collector+statsd.
func runIsolated(ctx context.Context, selected []scenarios.ScenarioDef,
	pgDSN, outputDir, collectorBin, loadgenBin, collectorURL, statsdBin, statsdContainers,
	composeDir string, drainWait bool, drainIdleSecs, drainTimeoutSecs int) {

	for _, scenario := range selected {
		log.Printf("scenarios: === isolated run: %s ===", scenario.Name)

		if err := restartStack(ctx, composeDir, pgDSN); err != nil {
			log.Fatalf("scenarios: restart stack for %s: %v", scenario.Name, err)
		}

		collectorCmd := startProcess(ctx, collectorBin,
			"--output", filepath.Join(outputDir, "metrics.jsonl"))
		if err := waitForCollector(collectorURL, 10*time.Second); err != nil {
			log.Printf("scenarios: collector not ready (continuing): %v", err)
		}

		var statsdCmd *exec.Cmd
		if statsdBin != "" {
			statsdCmd = startProcess(ctx, statsdBin,
				"--output", filepath.Join(outputDir, "docker_stats.jsonl"),
				"--containers", statsdContainers)
		}

		runner := &scenarios.Runner{
			CollectorURL: collectorURL,
			LoadgenBin:   loadgenBin,
			DSN:          pgDSN,
			OutputDir:    outputDir,
			ComposeDir:   composeDir,
		}

		if err := runner.Init(ctx); err != nil {
			log.Printf("scenarios: init error (continuing): %v", err)
		}

		if drainWait {
			runner.DrainWait(ctx, []string{"kaptanto", "kaptanto-rust"}, drainIdleSecs, drainTimeoutSecs)
		}

		if err := runner.Run(ctx, []scenarios.ScenarioDef{scenario}); err != nil {
			log.Printf("scenarios: run error for %s: %v", scenario.Name, err)
		}

		stopProcess(collectorCmd)
		stopProcess(statsdCmd)

		log.Printf("scenarios: === %s done ===", scenario.Name)
	}
}

// runLegacy is the original single-stack mode (used when --compose-dir is absent).
func runLegacy(ctx context.Context, selected []scenarios.ScenarioDef,
	pgDSN, outputDir, collectorBin, loadgenBin, collectorURL, statsdBin, statsdContainers string,
	drainWait bool, drainIdleSecs, drainTimeoutSecs int) {

	collectorCmd := startProcess(ctx, collectorBin,
		"--output", filepath.Join(outputDir, "metrics.jsonl"))
	defer stopProcess(collectorCmd)

	if err := waitForCollector(collectorURL, 5*time.Second); err != nil {
		log.Printf("scenarios: collector not ready within 5s (continuing): %v", err)
	}

	if statsdBin != "" {
		statsdCmd := startProcess(ctx, statsdBin,
			"--output", filepath.Join(outputDir, "docker_stats.jsonl"),
			"--containers", statsdContainers)
		defer stopProcess(statsdCmd)
	}

	runner := &scenarios.Runner{
		CollectorURL: collectorURL,
		LoadgenBin:   loadgenBin,
		DSN:          pgDSN,
		OutputDir:    outputDir,
	}

	if err := runner.Init(ctx); err != nil {
		log.Printf("scenarios: init error (continuing): %v", err)
	}

	if drainWait {
		runner.DrainWait(ctx, []string{"kaptanto", "kaptanto-rust"}, drainIdleSecs, drainTimeoutSecs)
	}

	if err := runner.Run(ctx, selected); err != nil {
		log.Printf("scenarios: run error: %v", err)
	}
}

// restartStack tears down the compose stack (wiping all volumes), starts the
// infrastructure services first (so pre-conditions SQL runs before sequin
// attempts to create its replication slot consumer), then starts the full
// stack and waits for the CDC services to become healthy.
func restartStack(ctx context.Context, composeDir, pgDSN string) error {
	log.Println("scenarios: stack: down -v")
	if err := composeRun(ctx, composeDir, "down", "-v"); err != nil {
		return fmt.Errorf("docker compose down -v: %w", err)
	}

	// Start infrastructure first: postgres must be healthy before we create
	// the sequin_bench replication slot, which sequin reads at startup.
	log.Println("scenarios: stack: starting infrastructure services")
	if err := composeRun(ctx, composeDir, "up", "-d",
		"postgres", "redis", "sequin-postgres", "peerdb-postgres"); err != nil {
		return fmt.Errorf("docker compose up infra: %w", err)
	}

	log.Println("scenarios: stack: waiting for postgres")
	if err := waitForContainer(ctx, "bench-postgres-1", 60*time.Second); err != nil {
		return fmt.Errorf("postgres not healthy: %w", err)
	}

	log.Println("scenarios: stack: running pre-conditions SQL")
	if err := runPreconditions(ctx, pgDSN); err != nil {
		return fmt.Errorf("pre-conditions SQL: %w", err)
	}

	// Now start the full stack — sequin will find the slot already present.
	log.Println("scenarios: stack: starting all services")
	if err := composeRun(ctx, composeDir, "up", "-d"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Wait for the three CDC services that have working healthchecks.
	// Debezium's healthcheck uses wget which is absent in its image; it will
	// remain "starting" indefinitely so we skip it — its logs confirm it
	// streams WAL correctly regardless.
	log.Println("scenarios: stack: waiting for CDC services to be healthy")
	for _, svc := range []string{"bench-kaptanto-1", "bench-kaptanto-rust-1", "bench-sequin-1"} {
		if err := waitForContainer(ctx, svc, 120*time.Second); err != nil {
			log.Printf("scenarios: stack: %s not healthy within timeout (continuing): %v", svc, err)
		}
	}

	log.Println("scenarios: stack: ready")
	return nil
}

// runPreconditions creates the bench_events table, the two publications, and
// the sequin_bench replication slot on the benchmark Postgres instance.
// Uses stdin so that all statements execute in a single psql invocation.
func runPreconditions(ctx context.Context, pgDSN string) error {
	const sql = `
CREATE TABLE IF NOT EXISTS bench_events (
    id TEXT NOT NULL PRIMARY KEY,
    payload TEXT NOT NULL DEFAULT '',
    _bench_ts TIMESTAMPTZ NOT NULL
);
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname='bench_pub') THEN
        CREATE PUBLICATION bench_pub FOR TABLE bench_events;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname='sequin_bench_pub') THEN
        CREATE PUBLICATION sequin_bench_pub FOR TABLE bench_events;
    END IF;
END $$;
SELECT pg_create_logical_replication_slot('sequin_bench', 'pgoutput')
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name='sequin_bench');
`
	cmd := exec.CommandContext(ctx, "psql", pgDSN)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, out)
	}
	return nil
}

// waitForContainer polls docker inspect until the named container reports
// health status "healthy", or until the timeout elapses.
func waitForContainer(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		out, err := exec.CommandContext(ctx, "docker", "inspect",
			"--format", "{{.State.Health.Status}}", name).Output()
		if err != nil {
			continue // container not yet created or inspect failed
		}
		if strings.TrimSpace(string(out)) == "healthy" {
			return nil
		}
	}
	return fmt.Errorf("container %s not healthy after %s", name, timeout)
}

// composeRun runs a docker compose subcommand in the given directory,
// streaming output to stderr.
func composeRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// startProcess starts a subprocess with the given binary and arguments,
// wiring stdout/stderr to the parent process. Returns the running *exec.Cmd.
func startProcess(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("scenarios: start %s: %v", bin, err)
	}
	return cmd
}

// stopProcess sends SIGTERM to the process and waits for it to exit.
// No-op if cmd is nil or its process was never started.
func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
}

// waitForCollector polls GET /scenario on the collector management API until
// it responds with 200 or the deadline is exceeded.
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

