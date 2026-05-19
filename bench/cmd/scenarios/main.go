package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/olucasandrade/kaptanto/bench/internal/scenarios"
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

		captureContainerLogs(ctx, "bench-kaptanto-kafka-1")
		captureContainerLogs(ctx, "bench-kaptanto-nats-1")
		captureContainerLogs(ctx, "bench-debezium-connect-1")

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
	// nats and redpanda are also started here so we can pre-create the
	// bench_cdc JetStream stream and public.bench_events Kafka topic before
	// kaptanto-nats and kaptanto-kafka start (both validate at init time).
	log.Println("scenarios: stack: starting infrastructure services")
	if err := composeRun(ctx, composeDir, "up", "-d",
		"postgres", "redis", "sequin-postgres", "peerdb-postgres", "nats", "redpanda"); err != nil {
		return fmt.Errorf("docker compose up infra: %w", err)
	}

	log.Println("scenarios: stack: waiting for postgres")
	if err := waitForContainer(ctx, "bench-postgres-1", 60*time.Second); err != nil {
		return fmt.Errorf("postgres not healthy: %w", err)
	}

	log.Println("scenarios: stack: waiting for nats")
	if err := waitForContainer(ctx, "bench-nats-1", 30*time.Second); err != nil {
		log.Printf("scenarios: nats not healthy (continuing): %v", err)
	}

	log.Println("scenarios: stack: waiting for redpanda")
	if err := waitForContainer(ctx, "bench-redpanda-1", 60*time.Second); err != nil {
		log.Printf("scenarios: redpanda not healthy (continuing): %v", err)
	}

	log.Println("scenarios: stack: running pre-conditions SQL")
	if err := runPreconditions(ctx, pgDSN); err != nil {
		return fmt.Errorf("pre-conditions SQL: %w", err)
	}

	log.Println("scenarios: stack: creating NATS JetStream stream")
	if err := runNATSPreconditions(ctx); err != nil {
		log.Printf("scenarios: NATS preconditions (continuing): %v", err)
	}

	log.Println("scenarios: stack: creating Kafka topic")
	if err := runKafkaPreconditions(ctx); err != nil {
		log.Printf("scenarios: Kafka preconditions (continuing): %v", err)
	}

	// Now start the full stack — sequin will find the slot already present,
	// kaptanto-nats will find bench_cdc stream, kaptanto-kafka will find the topic.
	log.Println("scenarios: stack: starting all services")
	if err := composeRun(ctx, composeDir, "up", "-d"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Wait for the CDC services that have working healthchecks.
	// Debezium Server's healthcheck uses wget which is absent in its image; it will
	// remain "starting" indefinitely so we skip it — its logs confirm it
	// streams WAL correctly regardless.
	log.Println("scenarios: stack: waiting for CDC services to be healthy")
	for _, svc := range []string{"bench-kaptanto-1", "bench-kaptanto-rust-1", "bench-sequin-1", "bench-kaptanto-kafka-1", "bench-kaptanto-nats-1"} {
		if err := waitForContainer(ctx, svc, 120*time.Second); err != nil {
			log.Printf("scenarios: stack: %s not healthy within timeout (continuing): %v", svc, err)
		}
	}
	// Debezium Connect (Kafka Connect JVM) takes longer to initialize.
	if err := waitForContainer(ctx, "bench-debezium-connect-1", 180*time.Second); err != nil {
		log.Printf("scenarios: stack: bench-debezium-connect-1 not healthy within timeout (continuing): %v", err)
	}

	log.Println("scenarios: stack: registering Debezium Connector")
	if err := runDebeziumConnectorPreconditions(ctx); err != nil {
		log.Printf("scenarios: Debezium Connector preconditions (continuing): %v", err)
	}

	log.Println("scenarios: stack: ready")
	return nil
}

// runDebeziumConnectorPreconditions registers the Debezium Postgres connector with
// the Kafka Connect REST API. The connector streams WAL events from bench_events
// to the bench-connect.public.bench_events Redpanda topic.
func runDebeziumConnectorPreconditions(ctx context.Context) error {
	const connectBase = "http://localhost:8083"
	const connectorName = "bench-postgres-connector"
	const connectorBody = `{
		"name": "bench-postgres-connector",
		"config": {
			"connector.class": "io.debezium.connector.postgresql.PostgresConnector",
			"database.hostname": "postgres",
			"database.port": "5432",
			"database.user": "bench",
			"database.password": "bench",
			"database.dbname": "bench",
			"topic.prefix": "bench-connect",
			"plugin.name": "pgoutput",
			"table.include.list": "public.bench_events",
			"slot.name": "debezium_connect",
			"publication.name": "bench_connect_pub",
			"publication.autocreate.mode": "disabled",
			"key.converter": "org.apache.kafka.connect.json.JsonConverter",
			"value.converter": "org.apache.kafka.connect.json.JsonConverter",
			"key.converter.schemas.enable": "false",
			"value.converter.schemas.enable": "false",
			"offset.flush.interval.ms": "0",
			"tasks.max": "1"
		}
	}`

	client := &http.Client{Timeout: 5 * time.Second}

	// Wait for the Connect REST API to be responsive (Kafka Connect takes 60–120s
	// to start: JVM init + Kafka broker connection + internal topic creation).
	log.Println("scenarios: Debezium Connector: waiting for REST API")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := client.Get(connectBase + "/connectors")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	// DELETE any existing connector registration (internal topics may survive
	// a stack restart if Redpanda state persists).
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
		connectBase+"/connectors/"+connectorName, nil)
	if resp, err := client.Do(req); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// POST the new connector config.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		connectBase+"/connectors",
		bytes.NewBufferString(connectorBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("register connector: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register connector: status %d: %s", resp.StatusCode, body)
	}
	log.Printf("scenarios: Debezium Connector registered (status %d)", resp.StatusCode)
	return nil
}

// runNATSPreconditions creates the JetStream stream bench_cdc for cdc.> subjects.
// kaptanto-nats requires this stream to exist before it will start publishing.
func runNATSPreconditions(ctx context.Context) error {
	nc, err := nats.Connect("nats://localhost:4222",
		nats.MaxReconnects(5),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("nats jetstream: %w", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "bench_cdc",
		Subjects: []string{"cdc.>"},
		Storage:  jetstream.MemoryStorage,
		MaxAge:   2 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("nats create stream bench_cdc: %w", err)
	}
	log.Println("scenarios: NATS stream bench_cdc ready")
	return nil
}

// captureContainerLogs prints the last 60 log lines of a container to stderr for diagnostics.
func captureContainerLogs(ctx context.Context, container string) {
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", "60", container)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	log.Printf("scenarios: === logs: %s ===", container)
	if err := cmd.Run(); err != nil {
		log.Printf("scenarios: docker logs %s: %v", container, err)
	}
	log.Printf("scenarios: === end logs: %s ===", container)
}

// runKafkaPreconditions creates the public.bench_events Kafka topic on Redpanda.
// kaptanto-kafka fails with UNKNOWN_TOPIC_OR_PARTITION if the topic is absent at startup.
func runKafkaPreconditions(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "bench-redpanda-1",
		"rpk", "topic", "create", "public.bench_events",
		"-p", "10", "-r", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "TOPIC_ALREADY_EXISTS") || strings.Contains(string(out), "already exists") {
			log.Println("scenarios: Kafka topic public.bench_events already exists")
			return nil
		}
		return fmt.Errorf("rpk topic create: %w: %s", err, out)
	}
	log.Printf("scenarios: Kafka topic public.bench_events created: %s", strings.TrimSpace(string(out)))
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
    IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname='bench_connect_pub') THEN
        CREATE PUBLICATION bench_connect_pub FOR TABLE bench_events;
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

