---
phase: 12-metrics-collector-and-scenarios
plan: "02"
subsystem: infra
tags: [docker, statsd, redpanda, debezium, cdc-benchmark, go, metrics]

# Dependency graph
requires:
  - phase: 11-harness-and-load-generator
    provides: bench/docker-compose.yml, bench module with loadgen, bench compose topology

provides:
  - bench/internal/statsd/poller.go: StatRecord type, parseCPUPct, parseVmRSS, readVmRSSFromHost, readCPUPct, RunPoller
  - bench/cmd/statsd/main.go: statsd binary CLI (--containers, --output, --interval)
  - bench/Dockerfile.statsd: two-stage docker:cli runtime for statsd container
  - bench/docker-compose.yml: redpanda v24.3.10 service and statsd service (pid:host)
  - bench/config/debezium/application.properties: Debezium sink reconfigured to HTTP collector endpoint

affects:
  - 12-03-scenarios
  - 13-benchmark-report-generator

# Tech tracking
tech-stack:
  added:
    - redpandadata/redpanda:v24.3.10 (Kafka-compatible broker for PeerDB Kafka output)
    - docker:cli runtime image (required for docker exec calls in statsd container)
  patterns:
    - host-proc pattern: docker inspect PID -> /proc/<pid>/status VmRSS (avoids distroless exec failure)
    - pid:host compose service for cross-container /proc access
    - NDJSON append-write poller with ticker + per-container goroutines

key-files:
  created:
    - bench/internal/statsd/poller.go
    - bench/internal/statsd/poller_test.go
    - bench/cmd/statsd/main.go
    - bench/Dockerfile.statsd
  modified:
    - bench/docker-compose.yml
    - bench/config/debezium/application.properties

key-decisions:
  - "docker:cli runtime for Dockerfile.statsd — statsd calls exec.Command(docker) for inspect and stats; distroless lacks the docker CLI binary"
  - "pid: host on statsd compose service — docker inspect returns host PIDs; only visible in host process namespace on Docker Desktop"
  - "vmrss_kb JSON field name (not rss_kb) — Phase 13 report generator reads this field by name from docker_stats.jsonl"
  - "Debezium sink switched redis->http pointing collector:8081/ingest/debezium — without this MET-02 is unsatisfiable (zero events reach collector)"
  - "redpanda v24.3.10 added as explicit service — required for PeerDB Kafka output benchmark scenario"

patterns-established:
  - "Poller pattern: ticker loop + per-container WaitGroup goroutines, error-tolerant (log and continue), json.Encoder appends to file"
  - "Pure parsing helpers (parseCPUPct, parseVmRSS) exported for unit testing without Docker"

requirements-completed:
  - MET-04

# Metrics
duration: 3min
completed: 2026-03-21
---

# Phase 12 Plan 02: Stats Poller, Redpanda, and Debezium HTTP Sink Summary

**Docker container stats poller binary (CPU% via docker stats, VmRSS via host /proc), Redpanda Kafka service, and Debezium sink reconfigured from redis to HTTP collector endpoint**

## Performance

- **Duration:** ~3 min
- **Started:** 2026-03-21T03:14:56Z
- **Completed:** 2026-03-21T03:17:10Z
- **Tasks:** 2
- **Files modified:** 6

## Accomplishments

- Stats poller library (bench/internal/statsd) with TDD: parseCPUPct and parseVmRSS unit-tested without Docker, RunPoller loop handles per-container errors gracefully
- statsd binary with --containers/--output/--interval flags and signal-aware shutdown
- Dockerfile.statsd using docker:cli runtime so exec.Command("docker") calls succeed inside container
- docker-compose.yml extended with Redpanda v24.3.10 (PeerDB Kafka output) and statsd service with pid:host
- Debezium sink switched from redis to HTTP, routing events to collector:8081/ingest/debezium (MET-02 enablement)

## Task Commits

Each task was committed atomically:

1. **Task 1: Stats poller core library (RED)** - `824c118` (test)
2. **Task 1: Stats poller core library (GREEN)** - `58c2fa5` (feat)
3. **Task 2: statsd binary, compose additions, Debezium sink reconfiguration** - `369ed62` (feat)

**Plan metadata:** (docs commit follows)

_Note: TDD tasks have two commits (test RED → feat GREEN)_

## Files Created/Modified

- `bench/internal/statsd/poller.go` - StatRecord type, parseCPUPct, parseVmRSS, readVmRSSFromHost, readCPUPct, RunPoller
- `bench/internal/statsd/poller_test.go` - Unit tests for parse helpers (no Docker required)
- `bench/cmd/statsd/main.go` - CLI entry point with --containers/--output/--interval flags
- `bench/Dockerfile.statsd` - Two-stage build: golang:1.25-alpine builder, docker:cli runtime
- `bench/docker-compose.yml` - Added redpanda and statsd services
- `bench/config/debezium/application.properties` - Switched sink: redis -> http (collector endpoint)

## Decisions Made

- docker:cli runtime for Dockerfile.statsd: statsd calls exec.Command("docker") for both readVmRSSFromHost (docker inspect) and readCPUPct (docker stats). The docker CLI binary is absent from distroless/static, causing all poll iterations to fail silently. docker:cli is the correct runtime.
- pid: "host" on statsd compose service: docker inspect returns host PIDs from the Docker Desktop VM's process namespace. Without pid:host, the statsd container cannot read /proc/<host-pid>/status.
- vmrss_kb JSON field name preserved exactly: Phase 13's report generator reads this field by name. Using rss_kb would yield zero memory readings in all benchmark reports.
- Debezium sink redis -> http: was a blocker for MET-02 (Debezium event count in collector). The original redis sink wrote events to a blackhole; no events reached the collector without this change.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- statsd poller produces docker_stats.jsonl with container/ts/cpu_pct/vmrss_kb fields
- Redpanda available in compose network for PeerDB Kafka output scenario
- Debezium events now routed to collector - MET-02 unblocked
- Ready for Phase 12-03: benchmark scenarios (loadgen run scripts, collector scenario wiring)

---
*Phase: 12-metrics-collector-and-scenarios*
*Completed: 2026-03-21*
