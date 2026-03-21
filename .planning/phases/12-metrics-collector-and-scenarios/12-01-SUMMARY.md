---
phase: 12-metrics-collector-and-scenarios
plan: "01"
subsystem: infra
tags: [go, ndjson, cdc-benchmark, sse, kafka, debezium, sequin, peerdb, kaptanto, metrics, collector]

# Dependency graph
requires:
  - phase: 11-harness-and-load-generator
    provides: bench Go module (github.com/kaptanto/kaptanto/bench), bench/cmd/loadgen pattern, bench/ docker topology

provides:
  - bench/internal/collector/writer.go: EventRecord struct and RunWriter channel-serialized NDJSON writer
  - bench/internal/collector/adapters/kaptanto.go: SSE adapter with ParseKaptantoLine, RunKaptanto
  - bench/internal/collector/adapters/debezium.go: HTTP POST handler DebeziumHandler — 200 before processing
  - bench/internal/collector/adapters/sequin.go: batch push handler SequinHandler — one record per data[] entry
  - bench/internal/collector/adapters/peerdb.go: franz-go Kafka consumer with ExtractBenchTS (nested/flat JSON)
  - bench/cmd/collector/main.go: collector binary — starts all adapters, fan-out goroutine, management API

affects:
  - 12-02-statsd-and-infra
  - 12-03-scenarios

# Tech tracking
tech-stack:
  added:
    - github.com/twmb/franz-go v1.20.7 — franz-go Kafka client for PeerDB adapter
    - github.com/klauspost/compress v1.18.4 — indirect via franz-go
    - github.com/pierrec/lz4/v4 v4.1.25 — indirect via franz-go
  patterns:
    - Channel-serialized writer: single goroutine owns file writes, all adapters send to buffered chan
    - Fan-out goroutine pattern: adapters -> adapterCh -> fan-out (updates lastSeen) -> records -> writer
    - Always-200 webhook pattern: Debezium/Sequin handlers write status before processing to prevent retry floods
    - atomic.Value for scenario tag: adapters read current scenario atomically on each event, no channel coupling
    - RFC3339Nano-then-RFC3339 fallback: parseBenchTS used consistently across all adapters

key-files:
  created:
    - bench/internal/collector/writer.go
    - bench/internal/collector/writer_test.go
    - bench/internal/collector/adapters/kaptanto.go
    - bench/internal/collector/adapters/debezium.go
    - bench/internal/collector/adapters/sequin.go
    - bench/internal/collector/adapters/peerdb.go
    - bench/internal/collector/adapters/adapters_test.go
    - bench/cmd/collector/main.go
  modified:
    - bench/go.mod
    - bench/go.sum

key-decisions:
  - "RunKaptanto/RunPeerDB instead of shared Run name: both functions are in the same adapters package; disambiguating avoids compile error without splitting into sub-packages"
  - "Fan-out goroutine between adapterCh and writer records channel: keeps lastSeen map updates consistent without blocking adapters or requiring mutex on the hot write path"
  - "Always-200 before processing in Debezium/Sequin handlers: prevents retry floods from CDC sinks treating non-2xx as retriable errors (Pitfall documented in plan)"
  - "ExtractBenchTS walks top-level, then after, then record keys: PeerDB Kafka messages vary by table/CDC mode so the walker handles all observed formats"
  - "Management API returns empty string (not 404) for unknown tool in /scenario/last-event: avoids crash-recovery poll logic treating 404 as fatal"

patterns-established:
  - "Adapter pattern: each CDC tool gets its own .go file in adapters/ with a exported Run* or Handler function plus a testable pure function for parsing"
  - "Test infrastructure: httptest.NewRecorder for handler tests, os.CreateTemp for file-based output tests, no live network required"

requirements-completed: [MET-01, MET-02, MET-03]

# Metrics
duration: 5min
completed: 2026-03-21
---

# Phase 12 Plan 01: Metrics Collector and Adapters Summary

**NDJSON metrics collector binary with four CDC-tool adapters (Kaptanto SSE, Debezium HTTP, Sequin push, PeerDB Kafka) writing end-to-end latency records to metrics.jsonl**

## Performance

- **Duration:** ~5 min
- **Started:** 2026-03-21T03:14:45Z
- **Completed:** 2026-03-21T03:19:32Z
- **Tasks:** 3
- **Files created:** 8, **Files modified:** 2

## Accomplishments

- EventRecord type and channel-serialized NDJSON writer with 5 tests including concurrent no-interleave verification
- Four per-tool CDC adapters: SSE (Kaptanto), HTTP POST (Debezium, Sequin), Kafka consumer (PeerDB) with 8 unit tests
- Collector binary CLI with management API (GET/POST /scenario, GET /scenario/last-event), fan-out goroutine for lastSeen tracking

## Task Commits

Each task was committed atomically:

1. **Task 1: EventRecord type and channel-serialized NDJSON writer** - `f6f4184` (feat)
2. **Task 2: Four per-tool adapters** - `e65a39c` (feat)
3. **Task 3: Collector binary CLI with management API** - `ddec510` (feat)

_Note: All TDD tasks followed RED (test) -> GREEN (impl) cycle within each commit._

## Files Created/Modified

- `bench/internal/collector/writer.go` - EventRecord struct, RunWriter loop with select on ctx/channel
- `bench/internal/collector/writer_test.go` - 5 writer tests including concurrent no-interleave
- `bench/internal/collector/adapters/kaptanto.go` - SSE client, ParseKaptantoLine exported for tests
- `bench/internal/collector/adapters/debezium.go` - HTTP handler, 200-first, DebeziumHandler exported
- `bench/internal/collector/adapters/sequin.go` - Batch push handler, SequinHandler exported
- `bench/internal/collector/adapters/peerdb.go` - franz-go consumer, ExtractBenchTS exported for tests
- `bench/internal/collector/adapters/adapters_test.go` - 8 adapter tests (httptest-based, no live network)
- `bench/cmd/collector/main.go` - CLI entry point, fan-out goroutine, management API on --management-port
- `bench/go.mod` - Added github.com/twmb/franz-go v1.20.7
- `bench/go.sum` - Updated checksums

## Decisions Made

- **RunKaptanto/RunPeerDB naming:** Both `Run` functions in same package caused compile error. Renamed to `RunKaptanto` and `RunPeerDB` without splitting into sub-packages.
- **Fan-out goroutine:** adapterCh -> fan-out updates lastSeen map -> records channel -> writer. Keeps management API reads consistent without blocking adapter goroutines.
- **Always-200 pattern:** Debezium and Sequin handlers call `w.WriteHeader(http.StatusOK)` before reading the body. Prevents retry floods from CDC sinks treating non-2xx as retriable.
- **ExtractBenchTS walk order:** top-level, then `after`, then `record` — covers all observed PeerDB Kafka message formats without configuration.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Renamed Run functions to avoid package-level redeclaration**
- **Found during:** Task 2 (four per-tool adapters compilation)
- **Issue:** Both kaptanto.go and peerdb.go defined `func Run(...)` in the same `adapters` package, causing compile error
- **Fix:** Renamed to `RunKaptanto` and `RunPeerDB`; updated main.go references accordingly
- **Files modified:** bench/internal/collector/adapters/kaptanto.go, bench/internal/collector/adapters/peerdb.go, bench/cmd/collector/main.go
- **Verification:** `go build ./internal/collector/adapters/...` and all tests pass
- **Committed in:** e65a39c (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 - Bug)
**Impact on plan:** Necessary compile fix. No scope creep. debezium.go retains exported `Handler` alias matching plan artifact spec.

## Issues Encountered

None beyond the naming conflict documented above.

## User Setup Required

None - no external service configuration required for the collector binary itself. Live adapters require the respective CDC tools running (Kaptanto, Debezium, Sequin, PeerDB/Redpanda) but the collector starts cleanly without them and retries connections.

## Next Phase Readiness

- Collector binary is complete and ready for use in scenario scripts
- Management API endpoint `/scenario` enables scenario orchestration from 12-03
- All 13 tests (5 writer + 8 adapter) pass with -race flag
- `go build ./...` exits 0 with franz-go in go.mod
- 12-02 statsd binary and docker-compose additions can proceed independently

---
*Phase: 12-metrics-collector-and-scenarios*
*Completed: 2026-03-21*
