---
phase: 13-reporter
plan: 01
subsystem: testing
tags: [go, ndjson, parsing, percentiles, aggregation, bufio, slices, reporter]

# Dependency graph
requires:
  - phase: 12-metrics-collector-and-scenarios
    provides: EventRecord written to metrics.jsonl, StatRecord written to docker_stats.jsonl
provides:
  - ParseMetrics(path) returning *Accumulator with latencies, event counts, min/max timestamps, scenario windows, recovery times
  - ParseStats(path) returning []StatRecord
  - Aggregate(*Accumulator, []StatRecord) returning *ReportData with Tools, Scenarios, Stats, RecoverySeconds
  - ScenarioStats struct with ThroughputEPS, P50us, P95us, P99us, AvgCPUPct, AvgRSSMB
  - ReportData struct ready for plan 13-02 renderer consumption
affects:
  - 13-reporter plan 02 (renderer consumes ReportData)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "NDJSON type discrimination: unmarshal to map[string]json.RawMessage, check scenario_event key presence before branching"
    - "Nearest-rank percentile: idx = ceil(p/100 * N) - 1, clamped to [0, N-1]"
    - "bufio.Scanner with 1 MB buffer to handle lines exceeding the default 64 KB limit"
    - "StatRecord-to-scenario assignment by TS window matching against ScenarioWindow [Start, End]"
    - "Canonical ordering: iterate fixed tool/scenario slices, include only entries present in data"

key-files:
  created:
    - bench/internal/reporter/parser.go
    - bench/internal/reporter/parser_test.go
    - bench/internal/reporter/aggregator.go
    - bench/internal/reporter/aggregator_test.go
  modified: []

key-decisions:
  - "StatRecord has no scenario field — window assignment done by TS matching against ScenarioWindow derived from boundary markers in metrics.jsonl"
  - "Accumulator.Latencies sorted in-place inside Aggregate (not ParseMetrics) — parsing is accumulation-only; sorting belongs to aggregation"
  - "No external dependencies added — slices.Sort and math.Ceil are stdlib (Go 1.21+, module uses Go 1.25)"
  - "StatRecord type defined in reporter package (not imported from statsd) — avoids cross-package import; field names match JSON tags exactly"

patterns-established:
  - "Parse → Accumulate → Aggregate separation: ParseMetrics/ParseStats produce raw data; Aggregate owns sorting and computation"
  - "TDD RED-GREEN: failing tests committed before implementation; one commit per phase per feature"

requirements-completed: [RPT-01, RPT-02]

# Metrics
duration: 3min
completed: 2026-03-21
---

# Phase 13 Plan 01: Reporter Data Pipeline Summary

**NDJSON parser with three-way type discrimination and nearest-rank percentile aggregator producing a fully-populated ReportData struct from metrics.jsonl and docker_stats.jsonl**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-21T08:56:49Z
- **Completed:** 2026-03-21T08:59:35Z
- **Tasks:** 2 (Parser + Aggregator, each via TDD RED/GREEN)
- **Files modified:** 4 created

## Accomplishments

- ParseMetrics reads metrics.jsonl with a 1 MB scanner buffer, discriminates all three record types (EventRecord, boundary marker, recovery marker), and populates an Accumulator with latencies, event counts, min/max timestamps, scenario windows, and recovery times
- ParseStats reads docker_stats.jsonl into []StatRecord with no discrimination needed
- Aggregate sorts latency slices, computes p50/p95/p99 via nearest-rank, computes throughput guarding against zero count/duration, assigns StatRecords to scenarios by TS window, computes mean CPU% and mean RSS-MB per (container, scenario), and returns a fully-populated ReportData
- 14 unit tests passing: 5 parser tests + 9 aggregator tests covering edge cases (empty slice, single element, zero count, outside-window records, canonical ordering)

## Task Commits

Each task was committed atomically via TDD RED then GREEN:

1. **Parser RED** - `b1d8583` (test)
2. **Parser GREEN** - `17ef66f` (feat)
3. **Aggregator RED** - `2b2f1ea` (test)
4. **Aggregator GREEN** - `4993f35` (feat)

_TDD plan: 4 commits total (2 test + 2 feat)_

## Files Created/Modified

- `bench/internal/reporter/parser.go` - ParseMetrics and ParseStats functions; Accumulator, ScenarioWindow, StatRecord types
- `bench/internal/reporter/parser_test.go` - 5 tests covering 5-line fixture, empty file, large-line buffer, ParseStats decoding
- `bench/internal/reporter/aggregator.go` - Aggregate function; ScenarioStats, ReportData types; percentile and mean helpers
- `bench/internal/reporter/aggregator_test.go` - 9 tests covering percentile formula, throughput, stat assignment, mean CPU, recovery pass-through, canonical ordering

## Decisions Made

- StatRecord defined in reporter package (not imported from statsd) — avoids cross-package import while maintaining identical JSON field names
- Latencies sorted inside Aggregate (not ParseMetrics) — parse phase is accumulation-only; sorting is an aggregation concern
- Window assignment by TS matching: a StatRecord matches at most one scenario window; the first match wins (scenarios run sequentially, windows do not overlap)
- No external dependencies added to bench/go.mod

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- ReportData struct is fully populated with Tools, Scenarios, Stats (map[tool][scenario]ScenarioStats), and RecoverySeconds
- Plan 13-02 renderer can import reporter package and call Aggregate directly
- ScenarioStats exports ThroughputEPS, P50us, P95us, P99us, AvgCPUPct, AvgRSSMB — all fields the renderer needs for Chart.js datasets

---
*Phase: 13-reporter*
*Completed: 2026-03-21*

## Self-Check: PASSED

- bench/internal/reporter/parser.go: FOUND
- bench/internal/reporter/aggregator.go: FOUND
- bench/internal/reporter/parser_test.go: FOUND
- bench/internal/reporter/aggregator_test.go: FOUND
- Commit b1d8583: FOUND
- Commit 17ef66f: FOUND
- Commit 2b2f1ea: FOUND
- Commit 4993f35: FOUND
