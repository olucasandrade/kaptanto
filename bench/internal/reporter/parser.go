// Package reporter provides NDJSON parsing and statistical aggregation for
// benchmark results produced by bench/internal/collector and bench/internal/statsd.
package reporter

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// key is the internal grouping key for per-(tool,scenario) accumulators.
type key struct {
	tool     string
	scenario string
}

// ScenarioWindow holds the wall-clock start and end timestamps for a scenario
// run, derived from boundary marker records in metrics.jsonl.
type ScenarioWindow struct {
	Start time.Time
	End   time.Time
}

// Accumulator holds the raw parsed data from metrics.jsonl.
// It is populated by ParseMetrics and consumed by Aggregate.
type Accumulator struct {
	Latencies       map[key][]int64
	EventCounts     map[key]int64
	MinTS           map[key]time.Time
	MaxTS           map[key]time.Time
	ScenarioWindows map[string]ScenarioWindow
	RecoveryTime    map[string]float64
}

// StatRecord mirrors bench/internal/statsd.StatRecord for use in the reporter.
// JSON field names are authoritative and must match docker_stats.jsonl output.
type StatRecord struct {
	Container string    `json:"container"`
	TS        time.Time `json:"ts"`
	CPUPCT    float64   `json:"cpu_pct"`
	VmRSSKB   int64     `json:"vmrss_kb"`
}

// eventRecord is the internal struct used to decode EventRecord lines.
type eventRecord struct {
	Tool      string    `json:"tool"`
	Scenario  string    `json:"scenario"`
	ReceiveTS time.Time `json:"receive_ts"`
	BenchTS   time.Time `json:"bench_ts"`
	LatencyUS int64     `json:"latency_us"`
}

// rawEvent is an intermediate buffer entry used for bench_ts-based attribution.
type rawEvent struct {
	tool      string
	benchTS   time.Time
	receiveTS time.Time
	latencyUS int64
	scenario  string // original tag; fallback when benchTS is zero
}

// scenarioForBenchTS returns the scenario name whose window contains benchTS,
// or "" if benchTS falls outside all known windows.
func scenarioForBenchTS(windows map[string]ScenarioWindow, benchTS time.Time) string {
	for name, w := range windows {
		if w.Start.IsZero() || w.End.IsZero() {
			continue
		}
		if !benchTS.Before(w.Start) && !benchTS.After(w.End) {
			return name
		}
	}
	return ""
}

// ParseMetrics reads path (metrics.jsonl) and returns an Accumulator populated
// with latency samples, event counts, min/max timestamps, scenario time windows,
// and recovery times. Malformed lines are skipped.
//
// Scenario attribution uses bench_ts (the Postgres-side insert timestamp) to
// assign each event to the scenario window in which the row was inserted, rather
// than the scenario tag that was active when the event was received. This gives
// accurate per-scenario numbers even when a consumer (e.g. kaptanto) has a
// delivery backlog that causes events to arrive in a later scenario window.
// If bench_ts is zero or falls outside all windows, the original scenario tag is
// used as a fallback.
func ParseMetrics(path string) (*Accumulator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	acc := &Accumulator{
		Latencies:       make(map[key][]int64),
		EventCounts:     make(map[key]int64),
		MinTS:           make(map[key]time.Time),
		MaxTS:           make(map[key]time.Time),
		ScenarioWindows: make(map[string]ScenarioWindow),
		RecoveryTime:    make(map[string]float64),
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	// First pass: collect scenario windows, recovery times, and buffer raw events.
	var events []rawEvent

	for scanner.Scan() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}

		if evField, ok := raw["scenario_event"]; ok {
			var evType string
			if err := json.Unmarshal(evField, &evType); err != nil {
				continue
			}

			switch evType {
			case "recovery":
				var tool string
				var secs float64
				if toolRaw, ok := raw["tool"]; ok {
					_ = json.Unmarshal(toolRaw, &tool)
				}
				if secsRaw, ok := raw["recovery_seconds"]; ok {
					_ = json.Unmarshal(secsRaw, &secs)
				}
				if tool != "" {
					acc.RecoveryTime[tool] = secs
				}
			case "start":
				var scenario string
				var ts time.Time
				if scenRaw, ok := raw["scenario"]; ok {
					_ = json.Unmarshal(scenRaw, &scenario)
				}
				if tsRaw, ok := raw["ts"]; ok {
					_ = json.Unmarshal(tsRaw, &ts)
				}
				if scenario != "" {
					win := acc.ScenarioWindows[scenario]
					win.Start = ts
					acc.ScenarioWindows[scenario] = win
				}
			case "end":
				var scenario string
				var ts time.Time
				if scenRaw, ok := raw["scenario"]; ok {
					_ = json.Unmarshal(scenRaw, &scenario)
				}
				if tsRaw, ok := raw["ts"]; ok {
					_ = json.Unmarshal(tsRaw, &ts)
				}
				if scenario != "" {
					win := acc.ScenarioWindows[scenario]
					win.End = ts
					acc.ScenarioWindows[scenario] = win
				}
			}
			continue
		}

		// No "scenario_event" field — treat as EventRecord.
		var rec eventRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Tool == "" && rec.Scenario == "" {
			continue
		}

		events = append(events, rawEvent{
			tool:      rec.Tool,
			benchTS:   rec.BenchTS,
			receiveTS: rec.ReceiveTS,
			latencyUS: rec.LatencyUS,
			scenario:  rec.Scenario,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Second pass: attribute each event to a scenario using bench_ts, then
	// aggregate into the Accumulator maps.
	for _, ev := range events {
		scenario := ""
		if !ev.benchTS.IsZero() {
			scenario = scenarioForBenchTS(acc.ScenarioWindows, ev.benchTS)
		}
		if scenario == "" {
			scenario = ev.scenario // fallback to original tag
		}
		if scenario == "" {
			continue
		}

		k := key{tool: ev.tool, scenario: scenario}
		acc.Latencies[k] = append(acc.Latencies[k], ev.latencyUS)
		acc.EventCounts[k]++

		if acc.MinTS[k].IsZero() || ev.receiveTS.Before(acc.MinTS[k]) {
			acc.MinTS[k] = ev.receiveTS
		}
		if ev.receiveTS.After(acc.MaxTS[k]) {
			acc.MaxTS[k] = ev.receiveTS
		}
	}

	return acc, nil
}

// ParseStats reads path (docker_stats.jsonl) and returns all StatRecord lines.
// Malformed lines are skipped.
func ParseStats(path string) ([]StatRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []StatRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		var rec StatRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
