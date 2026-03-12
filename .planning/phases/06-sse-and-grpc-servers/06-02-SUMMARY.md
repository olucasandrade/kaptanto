---
phase: 06-sse-and-grpc-servers
plan: "02"
subsystem: observability
tags: [prometheus, metrics, healthz, http, custom-registry]

# Dependency graph
requires:
  - phase: 05-router-and-stdout-output
    provides: Router and StdoutWriter as consumers that will record metric increments
provides:
  - KaptantoMetrics with custom Prometheus registry exposing /metrics in text format
  - HealthHandler for /healthz returning 200/ok or 503/JSON with failing probe details
  - HealthProbe interface for any component to register a named health check
affects:
  - 06-03-sse-server (will mount /metrics and /healthz on observability mux)
  - 06-04-grpc-server (will record kaptanto_events_delivered_total and kaptanto_errors_total)

# Tech tracking
tech-stack:
  added:
    - github.com/prometheus/client_golang v1.23.2
    - github.com/prometheus/client_model v0.6.2
    - github.com/prometheus/common v0.66.1
    - github.com/prometheus/procfs v0.16.1
  patterns:
    - Custom prometheus.Registry per KaptantoMetrics instance — no global registerer, no double-registration in tests
    - HealthProbe slice pattern — components register named probes; handler iterates and reports all failing ones
    - httptest.NewRecorder for unit tests, httptest.NewServer for integration test — no live port required

key-files:
  created:
    - internal/observability/metrics.go
    - internal/observability/metrics_test.go
    - internal/observability/health.go
    - internal/observability/health_test.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "KaptantoMetrics uses prometheus.NewRegistry() per instance — prevents double-registration panics in tests; no global DefaultRegisterer usage"
  - "HealthHandler accepts []HealthProbe slice at construction time — stateless after creation, safe for concurrent ServeHTTP calls"
  - "promhttp.HandlerFor(reg, HandlerOpts{Registry: reg}) — passes custom registry for both exposition and error counting"

patterns-established:
  - "Custom registry pattern: always prometheus.NewRegistry() not prometheus.DefaultRegisterer for testable metric structs"
  - "Health probe pattern: HealthProbe{Name, Check func() error} slice injected at NewHealthHandler — no global registry"

requirements-completed: [OBS-01, OBS-02]

# Metrics
duration: 2min
completed: 2026-03-12
---

# Phase 06 Plan 02: Observability (Metrics + Health) Summary

**Prometheus metrics endpoint with custom registry and /healthz health endpoint using named probes — both testable without live infrastructure**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-12T00:10:35Z
- **Completed:** 2026-03-12T00:12:43Z
- **Tasks:** 2 (TDD, each with RED + GREEN commits)
- **Files modified:** 6

## Accomplishments

- KaptantoMetrics with custom prometheus.Registry exposes all 5 spec metrics (events_delivered_total, consumer_lag_events, errors_total, source_lag_bytes, checkpoint_flushes_total) plus Go/process collectors
- HealthHandler implements http.Handler returning 200/ok or 503/JSON listing all failing probe names and their error strings
- 9 tests (4 metrics + 5 health) plus integration TestObservabilityServer verifying real HTTP round-trip on httptest.Server — all pass with CGO_ENABLED=0

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: KaptantoMetrics failing tests** - `fe7d578` (test)
2. **Task 1 GREEN: KaptantoMetrics implementation** - `5e139d8` (feat)
3. **Task 2 RED: HealthHandler failing tests** - `1372c1c` (test)
4. **Task 2 GREEN: HealthHandler implementation** - `211caa7` (feat)

_TDD tasks have RED (test) + GREEN (feat) commits per task_

## Files Created/Modified

- `internal/observability/metrics.go` - KaptantoMetrics struct with custom registry, 5 metric vectors, Handler() method
- `internal/observability/metrics_test.go` - 4 unit tests: no-panic construction, HTTP 200, events_delivered in output, consumer_lag in output
- `internal/observability/health.go` - HealthProbe, HealthStatus, HealthHandler with ServeHTTP 200/503 logic
- `internal/observability/health_test.go` - 5 unit tests + TestObservabilityServer integration test
- `go.mod` / `go.sum` - Added prometheus/client_golang v1.23.2 and transitive dependencies

## Decisions Made

- Custom prometheus.Registry per KaptantoMetrics instance — prevents double-registration panics in tests; no usage of global DefaultRegisterer
- HealthHandler accepts []HealthProbe at construction time — stateless after creation, safe for concurrent requests
- promhttp.HandlerFor(reg, HandlerOpts{Registry: reg}) — passes the custom registry for both metric exposition and internal error counting

## Deviations from Plan

**1. [Rule 3 - Blocking] prometheus/client_golang missing from go.mod as direct dependency**
- **Found during:** Task 1 GREEN (building metrics.go)
- **Issue:** go get had added package to go.mod as indirect; first build attempt failed with "no required module provides package"
- **Fix:** Ran `go get github.com/prometheus/client_golang/prometheus` to mark it direct
- **Files modified:** go.mod, go.sum
- **Verification:** `CGO_ENABLED=0 go build ./...` passes cleanly
- **Committed in:** 5e139d8 (Task 1 GREEN commit)

---

**Total deviations:** 1 auto-fixed (Rule 3 - blocking dependency resolution)
**Impact on plan:** Necessary fix for build to succeed. No scope creep.

## Issues Encountered

None beyond the blocking dependency deviation above.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- `internal/observability` package is complete and tested
- Wave 2 plans (SSE server, gRPC server) can import KaptantoMetrics and NewHealthHandler directly
- Observability mux pattern (net/http.ServeMux with /metrics + /healthz) demonstrated in TestObservabilityServer

---
*Phase: 06-sse-and-grpc-servers*
*Completed: 2026-03-12*
