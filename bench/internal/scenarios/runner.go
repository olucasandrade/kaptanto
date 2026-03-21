package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ScenarioDef describes a single benchmark scenario.
type ScenarioDef struct {
	Name        string
	LoadgenArgs []string // passed directly to loadgen binary
	PreWaitS    int      // seconds to wait before starting loadgen (warmup)
}

// Scenarios is the canonical ordered list of all five benchmark scenarios.
var Scenarios = []ScenarioDef{
	{
		Name:        "steady",
		LoadgenArgs: []string{"--mode", "steady", "--rate", "10000", "--duration", "60s"},
		PreWaitS:    30,
	},
	{
		Name:        "burst",
		LoadgenArgs: []string{"--mode", "burst"},
		PreWaitS:    0,
	},
	{
		Name:        "large-batch",
		LoadgenArgs: []string{"--mode", "large-batch"},
		PreWaitS:    0,
	},
	{
		Name:        "crash-recovery",
		LoadgenArgs: []string{"--mode", "steady", "--rate", "10000", "--duration", "120s"},
		PreWaitS:    0,
	},
	{
		Name:        "idle",
		LoadgenArgs: []string{"--mode", "idle", "--duration", "60s"},
		PreWaitS:    0,
	},
}

// Runner coordinates the collector subprocess, loadgen executions, and
// scenario boundary tagging.
type Runner struct {
	CollectorURL string // base URL for collector management API (e.g. http://localhost:8080)
	LoadgenBin   string // path to compiled loadgen binary
	DSN          string // Postgres DSN passed to loadgen
	OutputDir    string // directory for metrics.jsonl output
}

// Init creates PeerDB peer/mirror via psql and registers the Sequin push
// consumer. Errors are logged but do not abort — services may already be
// configured from a previous run.
func (r *Runner) Init(ctx context.Context) error {
	peerSQL := "CREATE PEER bench_redpanda FROM KAFKA WITH (bootstrap_servers = 'redpanda:9092');"
	mirrorSQL := "CREATE MIRROR bench_mirror FROM bench_postgres TO bench_redpanda FOR TABLE public.bench_events;"

	for _, sql := range []string{peerSQL, mirrorSQL} {
		cmd := exec.CommandContext(ctx, "psql",
			"-h", "localhost",
			"-p", "9900",
			"-U", "peerdb",
			"-d", "peerdb",
			"-c", sql,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("scenarios: init peerdb (ignored): %v: %s", err, out)
		}
	}

	// Register Sequin push consumer.
	// IMPORTANT: http_endpoint must use port 8082 (collector --sequin-port) and path /ingest/sequin.
	curlCmd := exec.CommandContext(ctx, "curl", "-s", "-X", "POST",
		"http://localhost:7376/api/http_push_consumers",
		"-H", "Content-Type: application/json",
		"-d", `{"stream_name":"bench_events","http_endpoint":"http://collector:8082/ingest/sequin"}`,
	)
	if out, err := curlCmd.CombinedOutput(); err != nil {
		log.Printf("scenarios: init sequin (ignored): %v: %s", err, out)
	}

	log.Println("scenarios: init complete")
	return nil
}

// Run executes the provided scenarios in sequence, tagging collector output per
// scenario and handling crash+recovery logic for the crash-recovery scenario.
func (r *Runner) Run(ctx context.Context, scenarios []ScenarioDef) error {
	for _, s := range scenarios {
		log.Printf("scenarios: starting %s", s.Name)

		if err := r.setScenarioTag(ctx, s.Name); err != nil {
			log.Printf("scenarios: setScenarioTag %s (ignored): %v", s.Name, err)
		}

		if err := r.writeMarker(s.Name, "start"); err != nil {
			log.Printf("scenarios: writeMarker start %s: %v", s.Name, err)
		}

		if s.PreWaitS > 0 {
			log.Printf("scenarios: warmup %ds before %s", s.PreWaitS, s.Name)
			select {
			case <-time.After(time.Duration(s.PreWaitS) * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		var runErr error
		if s.Name == "crash-recovery" {
			runErr = r.runCrashRecovery(ctx, s)
		} else {
			cmd := r.buildLoadgenCmd(ctx, s)
			runErr = cmd.Run()
		}
		if runErr != nil {
			log.Printf("scenarios: %s loadgen error (ignored): %v", s.Name, runErr)
		}

		if err := r.writeMarker(s.Name, "end"); err != nil {
			log.Printf("scenarios: writeMarker end %s: %v", s.Name, err)
		}

		log.Printf("scenarios: %s complete — draining 5s", s.Name)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// runCrashRecovery runs the crash+recovery scenario (SCN-04).
// It starts loadgen in the background, kills each tool container, restarts it,
// and polls the collector management API to detect recovery.
func (r *Runner) runCrashRecovery(ctx context.Context, s ScenarioDef) error {
	cmd := r.buildLoadgenCmd(ctx, s)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("crash-recovery: start loadgen: %w", err)
	}

	// Wait 30s for steady-state baseline before starting kills.
	log.Println("scenarios: crash-recovery: waiting 30s for steady state")
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}

	containers := []string{"kaptanto", "debezium", "sequin", "peerdb"}
	for _, container := range containers {
		killTime := time.Now()

		log.Printf("scenarios: crash-recovery: killing %s", container)
		killCmd := exec.CommandContext(ctx, "docker", "kill", "--signal", "SIGKILL", container)
		if out, err := killCmd.CombinedOutput(); err != nil {
			log.Printf("scenarios: crash-recovery: kill %s (ignored): %v: %s", container, err, out)
		}

		startCmd := exec.CommandContext(ctx, "docker", "start", container)
		if out, err := startCmd.CombinedOutput(); err != nil {
			log.Printf("scenarios: crash-recovery: start %s (ignored): %v: %s", container, err, out)
		}

		recoverySecs := r.pollRecovery(ctx, container, killTime)
		log.Printf("scenarios: crash-recovery: %s recovered in %.2fs", container, recoverySecs)

		rec := map[string]interface{}{
			"scenario_event":   "recovery",
			"tool":             container,
			"recovery_seconds": recoverySecs,
			"ts":               time.Now().UTC(),
		}
		if err := r.appendJSONLine(rec); err != nil {
			log.Printf("scenarios: crash-recovery: write recovery marker: %v", err)
		}
	}

	// Wait for loadgen to finish.
	if err := cmd.Wait(); err != nil {
		log.Printf("scenarios: crash-recovery: loadgen wait (ignored): %v", err)
	}

	return nil
}

// pollRecovery polls GET /scenario/last-event?tool=container every 500ms until
// last_receive_ts advances past killTime or 120s elapses.
// Returns elapsed seconds from killTime to first advance.
func (r *Runner) pollRecovery(ctx context.Context, tool string, killTime time.Time) float64 {
	deadline := time.Now().Add(120 * time.Second)
	url := r.CollectorURL + "/scenario/last-event?tool=" + tool

	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return time.Since(killTime).Seconds()
		case <-time.After(500 * time.Millisecond):
		}

		resp, err := client.Get(url)
		if err != nil {
			continue
		}

		var body struct {
			Tool          string `json:"tool"`
			LastReceiveTS string `json:"last_receive_ts"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		if body.LastReceiveTS == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, body.LastReceiveTS)
		if err != nil {
			continue
		}
		if ts.After(killTime) {
			return time.Since(killTime).Seconds()
		}
	}

	return time.Since(killTime).Seconds()
}

// setScenarioTag sends POST to the collector management API to update the
// current scenario tag. Returns an error if the response is non-200.
func (r *Runner) setScenarioTag(ctx context.Context, name string) error {
	url := r.CollectorURL + "/scenario?name=" + name

	var req *http.Request
	var err error
	if ctx != nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	} else {
		req, err = http.NewRequest(http.MethodPost, url, nil)
	}
	if err != nil {
		return fmt.Errorf("setScenarioTag: build request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("setScenarioTag: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("setScenarioTag: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// writeMarker appends a scenario boundary JSON line to metrics.jsonl.
func (r *Runner) writeMarker(scenario, event string) error {
	rec := map[string]interface{}{
		"scenario_event": event,
		"scenario":       scenario,
		"ts":             time.Now().UTC(),
	}
	return r.appendJSONLine(rec)
}

// appendJSONLine appends a single JSON line to outputDir/metrics.jsonl.
func (r *Runner) appendJSONLine(rec interface{}) error {
	path := filepath.Join(r.OutputDir, "metrics.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	return enc.Encode(rec)
}

// buildLoadgenCmd constructs the exec.Cmd for loadgen with DSN and scenario args.
func (r *Runner) buildLoadgenCmd(ctx context.Context, s ScenarioDef) *exec.Cmd {
	args := append([]string{"--dsn", r.DSN}, s.LoadgenArgs...)
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, r.LoadgenBin, args...)
	} else {
		cmd = exec.Command(r.LoadgenBin, args...)
	}
	return cmd
}
