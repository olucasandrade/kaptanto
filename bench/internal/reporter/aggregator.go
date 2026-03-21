package reporter

import (
	"math"
	"slices"
)

// canonicalTools defines the deterministic display order for tools.
var canonicalTools = []string{"kaptanto", "debezium", "sequin", "peerdb"}

// canonicalScenarios defines the deterministic display order for scenarios.
var canonicalScenarios = []string{"steady", "burst", "large-batch", "crash-recovery", "idle"}

// ScenarioStats holds the aggregated statistics for a single (tool, scenario) pair.
type ScenarioStats struct {
	ThroughputEPS float64
	P50us         int64
	P95us         int64
	P99us         int64
	AvgCPUPct     float64
	AvgRSSMB      float64
}

// ReportData is the fully-populated output of Aggregate, ready for rendering
// by the plan 13-02 renderer.
type ReportData struct {
	Tools           []string
	Scenarios       []string
	Stats           map[string]map[string]ScenarioStats
	RecoverySeconds map[string]float64
}

// Aggregate consumes the parsed Accumulator and raw StatRecord slice produced
// by ParseMetrics and ParseStats, and returns a fully-populated ReportData.
func Aggregate(acc *Accumulator, stats []StatRecord) *ReportData {
	rd := &ReportData{
		Stats:           make(map[string]map[string]ScenarioStats),
		RecoverySeconds: make(map[string]float64),
	}

	// Copy recovery times directly.
	for tool, secs := range acc.RecoveryTime {
		rd.RecoverySeconds[tool] = secs
	}

	// Determine which tools and scenarios are present in the data,
	// preserving canonical order.
	toolSet := make(map[string]bool)
	scenSet := make(map[string]bool)
	for k := range acc.Latencies {
		toolSet[k.tool] = true
		scenSet[k.scenario] = true
	}

	for _, t := range canonicalTools {
		if toolSet[t] {
			rd.Tools = append(rd.Tools, t)
		}
	}
	for _, s := range canonicalScenarios {
		if scenSet[s] {
			rd.Scenarios = append(rd.Scenarios, s)
		}
	}

	// Pre-build CPU and RSS sample buckets: map[tool]map[scenario][]samples
	// Each StatRecord is assigned to a scenario by checking its TS against
	// scenario time windows.
	cpuSamples := make(map[string]map[string][]float64)
	rssSamples := make(map[string]map[string][]float64)

	for _, rec := range stats {
		for scenario, win := range acc.ScenarioWindows {
			if win.Start.IsZero() || win.End.IsZero() {
				continue
			}
			if (rec.TS.Equal(win.Start) || rec.TS.After(win.Start)) &&
				(rec.TS.Equal(win.End) || rec.TS.Before(win.End)) {

				if cpuSamples[rec.Container] == nil {
					cpuSamples[rec.Container] = make(map[string][]float64)
				}
				if rssSamples[rec.Container] == nil {
					rssSamples[rec.Container] = make(map[string][]float64)
				}
				cpuSamples[rec.Container][scenario] = append(cpuSamples[rec.Container][scenario], rec.CPUPCT)
				rssSamples[rec.Container][scenario] = append(rssSamples[rec.Container][scenario], float64(rec.VmRSSKB)/1024.0)
				break // a record belongs to at most one scenario window
			}
		}
	}

	// Build ScenarioStats for each (tool, scenario) pair in the accumulator.
	for k, latencies := range acc.Latencies {
		tool := k.tool
		scenario := k.scenario

		var ss ScenarioStats

		// Percentiles — sort in place; Aggregate owns this data.
		slices.Sort(latencies)
		ss.P50us = percentile(latencies, 50)
		ss.P95us = percentile(latencies, 95)
		ss.P99us = percentile(latencies, 99)

		// Throughput.
		count := acc.EventCounts[k]
		if count > 0 {
			minTS := acc.MinTS[k]
			maxTS := acc.MaxTS[k]
			dur := maxTS.Sub(minTS).Seconds()
			if dur > 0 {
				ss.ThroughputEPS = float64(count) / dur
			}
		}

		// CPU and RSS averages.
		if cpuSlice, ok := cpuSamples[tool][scenario]; ok && len(cpuSlice) > 0 {
			ss.AvgCPUPct = mean(cpuSlice)
		}
		if rssSlice, ok := rssSamples[tool][scenario]; ok && len(rssSlice) > 0 {
			ss.AvgRSSMB = mean(rssSlice)
		}

		if rd.Stats[tool] == nil {
			rd.Stats[tool] = make(map[string]ScenarioStats)
		}
		rd.Stats[tool][scenario] = ss
	}

	return rd
}

// percentile returns the p-th percentile (0–100) of a sorted []int64 slice
// using the nearest-rank method: index = ceil(p/100 * N) - 1.
// Returns 0 for empty slices.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// mean returns the arithmetic mean of a float64 slice. Panics on empty slice;
// callers must guard with len check.
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
