package reporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseMetrics_FiveLineFixture tests that a known JSONL fixture with one of
// each record type routes correctly to the accumulators, including bench_ts-based
// scenario attribution.
//
// Attribution rules under test:
//   - kaptanto bench_ts=09:59:59 is before all windows → falls back to scenario tag "steady"
//   - debezium bench_ts=10:00:59 falls inside the burst window → attributed to "burst"
//     (even though debezium received the event inside the steady receive window)
func TestParseMetrics_FiveLineFixture(t *testing.T) {
	// steady window: 10:00:00–10:00:30  (kaptanto bench_ts 09:59:59 is before it → tag fallback)
	// burst  window: 10:00:45–10:01:30  (debezium bench_ts 10:00:59 falls here → bench_ts wins)
	fixture := strings.Join([]string{
		`{"tool":"kaptanto","scenario":"steady","receive_ts":"2026-03-21T10:00:00Z","bench_ts":"2026-03-21T09:59:59Z","latency_us":1000}`,
		`{"tool":"debezium","scenario":"steady","receive_ts":"2026-03-21T10:01:00Z","bench_ts":"2026-03-21T10:00:59Z","latency_us":2000}`,
		`{"scenario_event":"start","scenario":"steady","ts":"2026-03-21T10:00:00Z"}`,
		`{"scenario_event":"end","scenario":"steady","ts":"2026-03-21T10:00:30Z"}`,
		`{"scenario_event":"start","scenario":"burst","ts":"2026-03-21T10:00:45Z"}`,
		`{"scenario_event":"end","scenario":"burst","ts":"2026-03-21T10:01:30Z"}`,
		`{"scenario_event":"recovery","tool":"kaptanto","recovery_seconds":4.37,"ts":"2026-03-21T10:05:04Z"}`,
	}, "\n")

	path := writeTemp(t, fixture)
	acc, err := ParseMetrics(path)
	if err != nil {
		t.Fatalf("ParseMetrics error: %v", err)
	}

	// kaptanto: bench_ts outside all windows → falls back to scenario tag "steady"
	k1 := key{tool: "kaptanto", scenario: "steady"}
	if got := acc.EventCounts[k1]; got != 1 {
		t.Errorf("EventCounts[kaptanto,steady] = %d, want 1", got)
	}
	if len(acc.Latencies[k1]) != 1 || acc.Latencies[k1][0] != 1000 {
		t.Errorf("Latencies[kaptanto,steady] = %v, want [1000]", acc.Latencies[k1])
	}

	// debezium: bench_ts inside burst window → attributed to "burst" (not "steady" tag)
	k2 := key{tool: "debezium", scenario: "burst"}
	if got := acc.EventCounts[k2]; got != 1 {
		t.Errorf("EventCounts[debezium,burst] = %d, want 1 (bench_ts attribution)", got)
	}

	// ScenarioWindow for steady
	win, ok := acc.ScenarioWindows["steady"]
	if !ok {
		t.Fatal("ScenarioWindows[steady] not found")
	}
	wantStart, _ := time.Parse(time.RFC3339, "2026-03-21T10:00:00Z")
	wantEnd, _ := time.Parse(time.RFC3339, "2026-03-21T10:00:30Z")
	if !win.Start.Equal(wantStart) {
		t.Errorf("ScenarioWindows[steady].Start = %v, want %v", win.Start, wantStart)
	}
	if !win.End.Equal(wantEnd) {
		t.Errorf("ScenarioWindows[steady].End = %v, want %v", win.End, wantEnd)
	}

	// Recovery
	if got := acc.RecoveryTime["kaptanto"]; got != 4.37 {
		t.Errorf("RecoveryTime[kaptanto] = %v, want 4.37", got)
	}
}

// TestParseMetrics_EmptyFile verifies empty accumulators and no panic.
func TestParseMetrics_EmptyFile(t *testing.T) {
	path := writeTemp(t, "")
	acc, err := ParseMetrics(path)
	if err != nil {
		t.Fatalf("ParseMetrics error: %v", err)
	}
	if len(acc.Latencies) != 0 {
		t.Errorf("expected empty Latencies, got %v", acc.Latencies)
	}
	if len(acc.EventCounts) != 0 {
		t.Errorf("expected empty EventCounts, got %v", acc.EventCounts)
	}
	if len(acc.ScenarioWindows) != 0 {
		t.Errorf("expected empty ScenarioWindows, got %v", acc.ScenarioWindows)
	}
	if len(acc.RecoveryTime) != 0 {
		t.Errorf("expected empty RecoveryTime, got %v", acc.RecoveryTime)
	}
}

// TestParseMetrics_LargeLineBuffer verifies that a line exceeding the default
// 64 KB bufio.Scanner buffer (set to 1 MB) is handled without error.
func TestParseMetrics_LargeLineBuffer(t *testing.T) {
	// Build a line with latency_us set and a large padding field.
	// The padding pushes the line well beyond 64 KB.
	padding := strings.Repeat("x", 70*1024) // 70 KB padding
	line := `{"tool":"kaptanto","scenario":"steady","receive_ts":"2026-03-21T10:00:00Z","bench_ts":"2026-03-21T09:59:59Z","latency_us":999,"_pad":"` + padding + `"}`
	path := writeTemp(t, line)

	acc, err := ParseMetrics(path)
	if err != nil {
		t.Fatalf("ParseMetrics error on large line: %v", err)
	}
	k := key{tool: "kaptanto", scenario: "steady"}
	if acc.EventCounts[k] != 1 {
		t.Errorf("EventCounts[kaptanto,steady] = %d, want 1", acc.EventCounts[k])
	}
}

// TestParseStats tests that docker_stats.jsonl is decoded into StatRecords.
func TestParseStats(t *testing.T) {
	fixture := strings.Join([]string{
		`{"container":"kaptanto","ts":"2026-03-21T10:00:02Z","cpu_pct":2.31,"vmrss_kb":45200}`,
		`{"container":"debezium","ts":"2026-03-21T10:00:03Z","cpu_pct":5.0,"vmrss_kb":102400}`,
	}, "\n")

	path := writeTemp(t, fixture)
	stats, err := ParseStats(path)
	if err != nil {
		t.Fatalf("ParseStats error: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 StatRecords, got %d", len(stats))
	}
	if stats[0].Container != "kaptanto" {
		t.Errorf("stats[0].Container = %q, want kaptanto", stats[0].Container)
	}
	if stats[0].CPUPCT != 2.31 {
		t.Errorf("stats[0].CPUPCT = %v, want 2.31", stats[0].CPUPCT)
	}
	if stats[0].VmRSSKB != 45200 {
		t.Errorf("stats[0].VmRSSKB = %d, want 45200", stats[0].VmRSSKB)
	}
}

// TestParseStats_Empty verifies empty slice and no panic.
func TestParseStats_Empty(t *testing.T) {
	path := writeTemp(t, "")
	stats, err := ParseStats(path)
	if err != nil {
		t.Fatalf("ParseStats error: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("expected empty stats, got %v", stats)
	}
}

// writeTemp writes content to a temporary file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}
