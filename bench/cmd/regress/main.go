// Command regress compares bench/results/metrics.jsonl against a stored baseline
// and exits non-zero when kaptanto steady throughput or burst p50 regress
// beyond the configured tolerance (default ±15%).
package main

import (
	"fmt"
	"os"

	"github.com/olucasandrade/kaptanto/bench/internal/reporter"
)

func main() {
	metricsPath := envOr("METRICS_PATH", "./results/metrics.jsonl")
	baselinePath := envOr("BASELINE_PATH", "./results/baseline-metrics.json")
	tolerance := reporter.RegressionTolerance

	current, err := reporter.ReportDataFromMetrics(metricsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress: parse metrics: %v\n", err)
		os.Exit(2)
	}
	baseline, err := reporter.LoadBaselineMetrics(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress: load baseline: %v\n", err)
		os.Exit(2)
	}

	failures := reporter.CompareRegression(current, baseline, tolerance)
	if len(failures) == 0 {
		fmt.Println("regress: OK — kaptanto steady throughput and burst p50 within tolerance")
		return
	}
	for _, f := range failures {
		fmt.Fprintf(os.Stderr, "regress: %s\n", f.Error())
	}
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
