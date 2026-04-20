package reporter

import (
	"html/template"
	"math"
	"slices"
	"strconv"
	"strings"
)

var canonicalTools = []string{"kaptanto", "kaptanto-rust", "debezium", "sequin", "peerdb"}
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

// ReportData is the fully-populated output of Aggregate, ready for rendering.
// The renderer populates the template.JS fields (ChartJS, *Chart, Hardware,
// GeneratedAt) before executing the HTML template.
type ReportData struct {
	Tools           []string
	Scenarios       []string
	Stats           map[string]map[string]ScenarioStats
	RecoverySeconds map[string]float64

	// Renderer-populated fields (set by RenderHTML before template execution).
	ChartJS         template.JS // inlined chart.umd.min.js content
	ThroughputChart template.JS // JSON: ChartData
	P50Chart        template.JS
	P95Chart        template.JS
	P99Chart        template.JS
	CPUChart        template.JS
	RSSChart        template.JS
	RecoveryChart   template.JS
	Hardware        string // from --hardware flag
	GeneratedAt     string // RFC3339 timestamp
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

				tool := normalizeContainer(rec.Container)
				if cpuSamples[tool] == nil {
					cpuSamples[tool] = make(map[string][]float64)
				}
				if rssSamples[tool] == nil {
					rssSamples[tool] = make(map[string][]float64)
				}
				cpuSamples[tool][scenario] = append(cpuSamples[tool][scenario], rec.CPUPCT)
				rssSamples[tool][scenario] = append(rssSamples[tool][scenario], float64(rec.VmRSSKB)/1024.0)
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

		// Throughput — use scenario window duration (start→end marker) as denominator
		// so that tools with a delivery backlog (events arrive late in a burst) still
		// show correct eps. Falls back to receive span if no window markers exist.
		count := acc.EventCounts[k]
		if count > 0 {
			dur := 0.0
			if win, ok := acc.ScenarioWindows[scenario]; ok && !win.Start.IsZero() && !win.End.IsZero() {
				dur = win.End.Sub(win.Start).Seconds()
			}
			if dur <= 0 {
				dur = acc.MaxTS[k].Sub(acc.MinTS[k]).Seconds()
			}
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

// normalizeContainer maps a Docker container name (e.g. "bench-kaptanto-rust-1")
// to its canonical tool name (e.g. "kaptanto-rust"). It strips the compose
// project prefix ("bench-") and instance suffix ("-<number>"), then matches
// against canonicalTools longest-first so "kaptanto-rust" beats "kaptanto".
func normalizeContainer(container string) string {
	s := strings.TrimPrefix(container, "bench-")
	if i := strings.LastIndex(s, "-"); i >= 0 {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	// Sort canonical tools by descending length so longer names match first.
	sorted := make([]string, len(canonicalTools))
	copy(sorted, canonicalTools)
	slices.SortFunc(sorted, func(a, b string) int { return len(b) - len(a) })
	for _, t := range sorted {
		if s == t || strings.HasPrefix(s, t+"-") {
			return t
		}
	}
	return container // unknown container — keep as-is
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
