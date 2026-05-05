---
phase: 21-kafka-sink
plan: 03
subsystem: infra
tags: [kafka, franz-go, sink, cmd, pipeline, output-switch]

# Dependency graph
requires:
  - phase: 21-02
    provides: KafkaSinkConsumer with franz-go, SetMetrics/Ping/Close/Deliver interface

provides:
  - case "kafka": wired in root.go output switch with full pipeline assembly
  - nil-config guard returning error containing "sinks.kafka"
  - defer kafkaSink.Close() for persistent TCP connection lifecycle
  - obs server on cfg.Port with /metrics and /healthz
  - TestOutputMode_Kafka_MissingConfig and TestOutputMode_Kafka_InvalidMode cmd tests

affects: [22-pubsub-sink, 23-rabbitmq-sink]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Queue sink case pattern: nil-check cfg.Sinks.X, construct consumer, SetMetrics, rtr.Register, append HealthProbe, serve /metrics+/healthz on cfg.Port"
    - "Import alias kafkasink mirrors natssink/sqssink naming convention"

key-files:
  created: []
  modified:
    - internal/cmd/root.go
    - internal/cmd/root_test.go

key-decisions:
  - "kafkasink import alias mirrors natssink/sqssink convention — consistent naming for all sink packages"
  - "Kafka obs server listens on cfg.Port (not cfg.Port+1) — Kafka sink publishes to external broker, no TCP server needed beyond observability"
  - "defer kafkaSink.Close() required — unlike SQS (stateless HTTP), Kafka maintains persistent TCP connections to brokers"

patterns-established:
  - "Queue sink wiring in root.go: follow SQS/NATS pattern exactly — nil-check, construct, SetMetrics, Register, HealthProbe, obs server on cfg.Port"
  - "Cmd tests for output mode: use unreachable Postgres DSN so pipeline reaches output switch before DB connection attempt"

requirements-completed: [SNK-03]

# Metrics
duration: 8min
completed: 2026-05-05
---

# Phase 21 Plan 03: Kafka Sink root.go Wiring Summary

**case "kafka": wired into root.go with nil-config guard, defer Close, SetMetrics, Register, HealthProbe, and obs server on cfg.Port; cmd tests verify nil-config and invalid-mode error messages**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-05-05T18:16:00Z
- **Completed:** 2026-05-05T18:24:19Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments

- Wired `case "kafka":` into `internal/cmd/root.go` output switch following the exact SQS/NATS queue-sink pattern
- nil-config guard returns clear error mentioning `sinks.kafka`; `defer kafkaSink.Close()` ensures TCP connections are released on shutdown
- Updated `default:` case error message to include "kafka" in the valid modes list
- Added `TestOutputMode_Kafka_MissingConfig` and `TestOutputMode_Kafka_InvalidMode` to `root_test.go`; both pass with CGO_ENABLED=0
- Full `make test` passes across all 22 packages; `make verify-no-cgo` green for linux/amd64 and darwin/arm64

## Task Commits

Each task was committed atomically:

1. **Task 1: Add case "kafka": to root.go output switch** - `a797fb4` (feat)
2. **Task 2: Add cmd tests for kafka output mode** - `01c00b3` (feat)

**Plan metadata:** (docs commit below)

## Files Created/Modified

- `internal/cmd/root.go` - Added kafkasink import alias and case "kafka": block; updated default: error message
- `internal/cmd/root_test.go` - Added TestOutputMode_Kafka_MissingConfig and TestOutputMode_Kafka_InvalidMode

## Decisions Made

- kafkasink import alias mirrors natssink/sqssink — consistent naming pattern for all sink packages
- Obs server listens on cfg.Port (not cfg.Port+1) — Kafka sink publishes to external broker; no TCP server beyond observability
- defer kafkaSink.Close() required — Kafka maintains persistent TCP connections, unlike the stateless HTTP SQS sink

## Deviations from Plan

None - plan executed exactly as written. Task 1 changes were already present in the working tree (written outside the plan executor) but uncommitted; committed atomically as part of execution.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Phase 21 (Kafka Sink) is complete. All three plans (config, consumer, root wiring) are committed.
- Phase 22 (Pub/Sub Sink) can begin. The queue-sink wiring pattern is fully established: follow the same nil-check → construct → SetMetrics → Register → HealthProbe → obs server structure.
- Known concern: Pub/Sub emulator setup for integration tests is not fully resolved — address at Phase 22 planning start.

---
*Phase: 21-kafka-sink*
*Completed: 2026-05-05*

## Self-Check: PASSED

- FOUND: internal/cmd/root.go
- FOUND: internal/cmd/root_test.go
- FOUND: .planning/phases/21-kafka-sink/21-03-SUMMARY.md
- FOUND commit: a797fb4 (feat(21-03): wire case "kafka": into root.go output switch)
- FOUND commit: 01c00b3 (feat(21-03): add cmd tests for kafka output mode)
