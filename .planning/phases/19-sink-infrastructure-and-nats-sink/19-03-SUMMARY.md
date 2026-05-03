---
phase: 19-sink-infrastructure-and-nats-sink
plan: 03
subsystem: output
tags: [nats, jetstream, cdc, sink, healthz, metrics, observability]

# Dependency graph
requires:
  - phase: 19-01
    provides: NATSSinkConfig and SinksConfig in config.Config
  - phase: 19-02
    provides: NATSSinkConsumer with Deliver, Ping, SetMetrics, Close, ID

provides:
  - case "nats": branch in root.go output switch
  - NATSSinkConsumer registered with router via rtr.Register
  - NATS health probe at /healthz (nats probe appended to healthProbes)
  - /metrics and /healthz served on cfg.Port for nats output mode
  - Nil-config guard: clear error when sinks.nats block is missing
  - Updated default case error message to include nats in valid modes list
  - Two cmd tests validating nats config-validation code paths

affects: [Phase 20 SQS sink, Phase 21 Kafka sink, Phase 22 PubSub sink, Phase 23 RabbitMQ sink]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "NATS sink wired via same pattern as grpc: obs HTTP server on cfg.Port for /metrics and /healthz"
    - "Nil-pointer guard on optional sink config blocks: check cfg.Sinks.NATS == nil before constructing consumer"
    - "Health probe appended to slice then reconstructed in case block to avoid disturbing existing healthHandler"

key-files:
  created: []
  modified:
    - internal/cmd/root.go
    - internal/cmd/root_test.go

key-decisions:
  - "NATS obs server runs on cfg.Port (not cfg.Port+1) — NATS sink has no TCP server of its own, so cfg.Port is free for HTTP"
  - "Dereference *NATSSinkConfig when calling NewNATSSinkConsumer — function takes value type, cfg.Sinks.NATS is a pointer"
  - "Reconstruct healthHandler in nats case with updated probes slice rather than moving healthHandler construction — minimal diff"

patterns-established:
  - "Each queue sink case must: nil-check cfg.Sinks.<X>, construct consumer, SetMetrics, rtr.Register, append HealthProbe, serve /metrics + /healthz"

requirements-completed: [CFG-04, DLV-01, DLV-02, DLV-03, DLV-04, OBS-01, OBS-02, SNK-05]

# Metrics
duration: 7min
completed: 2026-05-04
---

# Phase 19 Plan 03: CLI Wiring Summary

**NATSSinkConsumer wired into root.go output switch with health probe, /metrics + /healthz on cfg.Port, nil-config guard, and updated default error message**

## Performance

- **Duration:** 7 min
- **Started:** 2026-05-03T23:21:21Z
- **Completed:** 2026-05-04T01:22:30Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Added `case "nats":` to the output switch in `runPipeline` — users can now run `kaptanto --output nats`
- Nil-config guard returns a clear error when `sinks.nats` block is absent from config
- NATS health probe appended to `healthProbes` and served at /healthz; /metrics also served on cfg.Port
- Default case error message updated to include `nats` in valid output modes list
- Two cmd-level tests validate both config-validation code paths without requiring a live NATS server

## Task Commits

Each task was committed atomically:

1. **Task 1: Wire case "nats": in root.go output switch** - `4c4c6dd` (feat)
2. **Task 2: Add root.go integration tests for nats output mode** - `a5d0ac4` (test)

**Plan metadata:** (docs commit follows)

## Files Created/Modified
- `internal/cmd/root.go` - Added natssink import, case "nats": with full wiring, updated default error message
- `internal/cmd/root_test.go` - Added TestOutputMode_Nats_MissingConfig and TestOutputMode_Nats_InvalidMode

## Decisions Made
- NATS obs server runs on `cfg.Port` (not `cfg.Port+1`) because the NATS sink publishes to an external broker — there is no TCP server on cfg.Port, so the /metrics + /healthz HTTP server can use it directly. This matches user expectations and keeps the port consistent.
- `NewNATSSinkConsumer` takes `config.NATSSinkConfig` by value (not pointer), so the call site dereferences `cfg.Sinks.NATS` after the nil check.
- Health handler is reconstructed inline in the nats case using the updated `healthProbes` slice rather than moving the `healthHandler := observability.NewHealthHandler(healthProbes)` line. This minimizes diff and avoids disturbing the existing SSE and gRPC cases that use the pre-built `healthHandler`.

## Deviations from Plan

None - plan executed exactly as written. One minor adaptation: the plan showed `NewNATSSinkConsumer("nats", natsCfg)` with a pointer argument, but the actual function signature (from Plan 02) takes a value `config.NATSSinkConfig`. The call was adjusted to dereference the pointer (`*natsCfg`) after the nil check. This is an alignment issue, not a deviation.

## Issues Encountered
None. Build succeeded on the first attempt. All tests passed immediately.

## User Setup Required
None - no external service configuration required for this plan. End-to-end NATS connectivity requires a running NATS JetStream server; that is a runtime deployment concern, not a code setup step.

## Next Phase Readiness
- Phase 19 complete: NATS sink infrastructure fully wired end-to-end
- Phase 20 (SQS sink) can use the same pattern: nil-check SQS config, construct consumer, SetMetrics, rtr.Register, append health probe, serve /metrics + /healthz
- The `case "nats":` template in root.go serves as the canonical pattern for Phases 20-23

---
*Phase: 19-sink-infrastructure-and-nats-sink*
*Completed: 2026-05-04*
