package reporter

import (
	"encoding/json"
	"fmt"
	"os"
)

// BaselineMetrics holds reference benchmark numbers for regression checks.
// Values are sourced from published docs (docs-benchmarks section) — not
// auto-synced from bench/results/REPORT.md without an editorial update.
type BaselineMetrics struct {
	Tools map[string]BaselineToolMetrics `json:"tools"`
}

// BaselineToolMetrics holds per-scenario reference stats for one tool.
type BaselineToolMetrics struct {
	Steady BaselineScenarioMetrics `json:"steady"`
	Burst  BaselineScenarioMetrics `json:"burst"`
}

// BaselineScenarioMetrics is the subset of ScenarioStats used for regression.
type BaselineScenarioMetrics struct {
	ThroughputEPS float64 `json:"throughput_eps,omitempty"`
	P50us         int64   `json:"p50_us,omitempty"`
}

// RegressionTolerance is the default ±15% band for benchmark regression checks.
const RegressionTolerance = 0.15

// RegressionFailure describes one metric that breached the tolerance band.
type RegressionFailure struct {
	Tool     string
	Scenario string
	Metric   string
	Baseline float64
	Current  float64
	MinOK    float64
	MaxOK    float64
}

func (f RegressionFailure) Error() string {
	return fmt.Sprintf("%s/%s %s: current=%.2f baseline=%.2f allowed=[%.2f,%.2f]",
		f.Tool, f.Scenario, f.Metric, f.Current, f.Baseline, f.MinOK, f.MaxOK)
}

// LoadBaselineMetrics reads a JSON baseline artifact (see bench/results/baseline-metrics.json).
func LoadBaselineMetrics(path string) (*BaselineMetrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bm BaselineMetrics
	if err := json.Unmarshal(data, &bm); err != nil {
		return nil, err
	}
	return &bm, nil
}

// ReportDataFromMetrics parses metrics.jsonl and aggregates stats for comparison.
func ReportDataFromMetrics(metricsPath string) (*ReportData, error) {
	acc, err := ParseMetrics(metricsPath)
	if err != nil {
		return nil, err
	}
	return Aggregate(acc, nil), nil
}

// CompareRegression checks kaptanto steady throughput and burst p50 against
// baseline with the given tolerance. Throughput must not drop below baseline×(1−t);
// p50 latency must not exceed baseline×(1+t).
func CompareRegression(current *ReportData, baseline *BaselineMetrics, tolerance float64) []RegressionFailure {
	if current == nil || baseline == nil {
		return nil
	}
	const tool = "kaptanto"
	bm, ok := baseline.Tools[tool]
	if !ok {
		return nil
	}

	var failures []RegressionFailure

	if bm.Steady.ThroughputEPS > 0 {
		cur := current.Stats[tool]["steady"].ThroughputEPS
		minOK := bm.Steady.ThroughputEPS * (1 - tolerance)
		if cur < minOK {
			failures = append(failures, RegressionFailure{
				Tool: tool, Scenario: "steady", Metric: "throughput_eps",
				Baseline: bm.Steady.ThroughputEPS, Current: cur,
				MinOK: minOK, MaxOK: bm.Steady.ThroughputEPS * (1 + tolerance),
			})
		}
	}

	if bm.Burst.P50us > 0 {
		cur := current.Stats[tool]["burst"].P50us
		maxOK := float64(bm.Burst.P50us) * (1 + tolerance)
		if float64(cur) > maxOK {
			failures = append(failures, RegressionFailure{
				Tool: tool, Scenario: "burst", Metric: "p50_us",
				Baseline: float64(bm.Burst.P50us), Current: float64(cur),
				MinOK: float64(bm.Burst.P50us) * (1 - tolerance), MaxOK: maxOK,
			})
		}
	}

	return failures
}
