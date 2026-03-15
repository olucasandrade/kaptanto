---
phase: 07-configuration-and-multi-source
plan: "03"
subsystem: cli
tags: [cobra, config, signal, graceful-shutdown, tdd]

# Dependency graph
requires:
  - phase: 07-01
    provides: config.Load, config.Defaults, config.Merge — YAML config package
  - phase: 07-02
    provides: ApplyColumnFilter, RowFilter — filter integration hooks
provides:
  - Real RunE in internal/cmd/root.go with guard check, config load, Merge, graceful shutdown
  - runPipeline stub that blocks on ctx.Done() and logs config on start
  - root_test.go tests for missing-source guard and config-file-not-found error
affects:
  - Phase 8 (HA) — runPipeline stub will be replaced with real wiring
  - Future plans adding subcommands to root.go

# Tech tracking
tech-stack:
  added: [os/signal.NotifyContext, syscall.SIGTERM]
  patterns: [12-factor flag-wins-over-file via config.Merge, ctx.Done graceful shutdown]

key-files:
  created: []
  modified:
    - internal/cmd/root.go
    - internal/cmd/root_test.go

key-decisions:
  - "runPipeline is a stub for Phase 7 integration; future phases replace it with real Phase 1-6 component wiring"
  - "Guard checks configPath == '' and sourceDSN == '' before Merge so post-merge validation catches --source explicitly set to empty"
  - "signal.NotifyContext wraps cmd.Context() so tests that inject context can control cancellation without signal dependency"

patterns-established:
  - "Config loading pattern: configPath non-empty → Load(configPath) else Defaults(), then Merge(cfg, cmd)"
  - "Post-merge validation: second cfg.Source == '' check catches YAML file with no source + no --source flag"

requirements-completed: [CFG-02, CFG-05, CFG-06]

# Metrics
duration: 3min
completed: 2026-03-15
---

# Phase 7 Plan 03: RunE Startup Wiring Summary

**cobra RunE replaced with real pipeline startup: guard validation, YAML config load, CLI flag merge via Changed(), signal.NotifyContext graceful shutdown, and runPipeline stub logging config on start**

## Performance

- **Duration:** 2 min 25 sec
- **Started:** 2026-03-15T02:36:29Z
- **Completed:** 2026-03-15T02:38:54Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 2

## Accomplishments
- Replaced RunE no-op placeholder with production guard + config load + Merge + shutdown logic
- Added runPipeline stub that logs `kaptanto starting` and blocks until SIGTERM/SIGINT
- Added 3 new tests: missing-source guard, empty --source guard, config-file-not-found error
- All 16 cmd tests pass; full `go test ./...` suite passes; binary builds CGO_ENABLED=0

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): add failing tests for RunE guard conditions** - `1b140e4` (test)
2. **Task 1 (GREEN): implement RunE startup wiring and runPipeline stub** - `32b1491` (feat)

_TDD task: RED commit followed by GREEN commit_

## Files Created/Modified
- `internal/cmd/root.go` - Replaced RunE no-op with real startup logic; added runPipeline stub; removed time sentinel; added imports (fmt, os, os/signal, syscall, config)
- `internal/cmd/root_test.go` - Added TestRunE_MissingSourceAndConfig, TestRunE_EmptySource, TestRunE_ConfigFileNotFound

## Decisions Made
- runPipeline is a stub that blocks on ctx.Done() — real Phase 1-6 component wiring deferred to Phase 8+
- Guard checks `configPath == "" && sourceDSN == ""` before Merge; post-merge adds a second `cfg.Source == ""` check to catch the edge case where --source is explicitly set to "" or config file has no source field
- signal.NotifyContext wraps cmd.Context() (not context.Background()) so test harnesses can inject contexts without real OS signals

## Deviations from Plan

None — plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness
- kaptanto binary is now runnable: accepts --source DSN or --config YAML, starts pipeline (stub), shuts down cleanly on SIGTERM/SIGINT
- Phase 7 integration deliverable complete; Phase 8 (HA) can replace runPipeline stub with real wiring
- No blockers

---
*Phase: 07-configuration-and-multi-source*
*Completed: 2026-03-15*
