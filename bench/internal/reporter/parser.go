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
	LatencyUS int64     `json:"latency_us"`
}

// ParseMetrics reads path (metrics.jsonl) and returns an Accumulator populated
// with latency samples, event counts, min/max timestamps, scenario time windows,
// and recovery times. Malformed lines are skipped.
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
			// All scenario_event lines are consumed; skip to next line.
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

		k := key{tool: rec.Tool, scenario: rec.Scenario}
		acc.Latencies[k] = append(acc.Latencies[k], rec.LatencyUS)
		acc.EventCounts[k]++

		if acc.MinTS[k].IsZero() || rec.ReceiveTS.Before(acc.MinTS[k]) {
			acc.MinTS[k] = rec.ReceiveTS
		}
		if rec.ReceiveTS.After(acc.MaxTS[k]) {
			acc.MaxTS[k] = rec.ReceiveTS
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
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
