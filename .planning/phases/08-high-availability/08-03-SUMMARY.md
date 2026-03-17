---
phase: 08-high-availability
plan: "03"
subsystem: infra
tags: [ha, leader-election, advisory-lock, checkpoint, postgres, cobra, health]

requires:
  - phase: 08-high-availability/08-01
    provides: PostgresStore Postgres-backed CheckpointStore (OpenPostgres, Ping)
  - phase: 08-high-availability/08-02
    provides: LeaderElector with RunStandby + advisory lock (NewLeaderElector, Close)
provides:
  - runPipeline HA branch replacing slog.Warn stub with full election + checkpoint wiring
  - Conditional PostgresStore checkout store when cfg.HA=true (HA-03: takeover resumes from shared LSN)
  - ha_lock health probe appended to /healthz when HA active
  - Updated --ha flag description (removes etcd reference, adds advisory lock mention)
affects: [09-mongodb, 10-rust-ffi, pipeline-assembly]

tech-stack:
  added: []
  patterns:
    - Incremental healthProbes slice build — append HA probe conditionally after slice initialisation
    - ckProbe closure pattern — wrap pgStore.Ping(ctx) into func() error to match HealthProbe.Check signature
    - HA-before-pipeline ordering — LeaderElector and PostgresStore opened before Badger/cursors so only the leader initialises pipeline components

key-files:
  created: []
  modified:
    - internal/cmd/root.go
    - internal/cmd/root_test.go

key-decisions:
  - "HA election placed before all pipeline components in runPipeline — guarantees only the leader opens the replication slot and writes checkpoints"
  - "pgStore.Ping wraps context.Background() into func() error closure — HealthProbe.Check signature has no context param; PostgresStore.Ping requires one"
  - "ckStore declared as CheckpointStore interface, ckProbe as func() error — allows both SQLiteStore and PostgresStore to be assigned without type assertions downstream"
  - "pgStore kept as *PostgresStore (not interface) in HA branch — needed for ha_lock probe closure and avoids double-wrapping"

patterns-established:
  - "HA guard: if cfg.HA block before step 1 (data dir) sets up election and pgStore, then conditional store selection below"
  - "Incremental health probe slice: build base probes as slice literal, append HA probe with if cfg.HA block before NewHealthHandler call"

requirements-completed: [HA-03]

duration: 6min
completed: 2026-03-17
---

# Phase 8 Plan 03: HA Pipeline Wiring Summary

**LeaderElector and PostgresStore wired into runPipeline HA branch: advisory lock acquisition blocks until leadership, shared Postgres checkpoint store used for takeover LSN resume (HA-03)**

## Performance

- **Duration:** 6 min
- **Started:** 2026-03-17T00:29:17Z
- **Completed:** 2026-03-17T00:34:44Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments

- Replaced `slog.Warn` HA stub with full election flow: `ha.NewLeaderElector` + `elector.RunStandby(ctx, 2*time.Second)` polls until lock acquired or context cancelled
- When `cfg.HA=true`: `checkpoint.OpenPostgres(ctx, cfg.Source)` replaces `checkpoint.Open(path)` — new leader resumes from shared LSN stored by old leader (closes HA-03)
- `ha_lock` health probe appended to `/healthz` when HA active: wraps `pgStore.Ping(context.Background())` into `func() error` closure
- Updated `--ha` flag description: "uses Postgres advisory locks" replaces "requires etcd"
- Non-HA path unchanged: zero behavioral difference in `cfg.HA=false` branch

## Task Commits

Each task was committed atomically:

1. **RED (TDD)** - `0d39ed1` (test): add failing tests for HA wiring and flag help text
2. **Task 1: Wire HA election and Postgres checkpoint into runPipeline** - `14472f2` (feat)
3. **Task 2: Smoke test HA flag behavior and update --ha flag help text** - covered in `0d39ed1` (RED) + `14472f2` (feat)

_Note: Task 2 tests were included in the RED commit; flag description update was included in the Task 1 GREEN commit._

## Files Created/Modified

- `internal/cmd/root.go` - HA election block, PostgresStore conditional selection, incremental healthProbes, updated --ha flag description
- `internal/cmd/root_test.go` - TestHAFlagHelpText, TestNonHAPathUnchanged (integration guard), TestHAFlagSkipsWithoutDSN

## Decisions Made

- `ckProbe` closure wraps `pgStore.Ping(context.Background())` — `HealthProbe.Check` is `func() error` but `PostgresStore.Ping` takes a context; wrapping keeps both consistent
- `ckStore` typed as `checkpoint.CheckpointStore` interface — allows both `*SQLiteStore` and `*PostgresStore` to be assigned, connector picks up correct impl at startup without type assertions
- HA block placed at top of `runPipeline` before data dir, Badger, and cursor store — only the leader should initialise those components (correctness invariant)

## Deviations from Plan

None — plan executed exactly as written. The `pgStore.Ping` signature mismatch with `HealthProbe.Check` was anticipated by the plan's note about wrapping.

## Issues Encountered

- Pre-existing flaky test `TestSSEServer_IndependentConsumers` in `internal/output/sse` fails occasionally under full suite run but passes when run in isolation. Unrelated to HA changes; logged to deferred items.

## User Setup Required

None — no external service configuration required. HA mode requires a live Postgres instance but this is operator-configured via `--source`.

## Next Phase Readiness

- HA-01, HA-02, HA-03 all closed — Phase 8 HA requirements complete
- Advisory lock + shared checkpoint store form the complete HA handoff: crash releases lock, standby acquires it within 2s, loads last LSN from `postgres_checkpoints` table
- Phase 9 (MongoDB) can build on the same pipeline pattern without HA changes

## Self-Check: PASSED

- FOUND: internal/cmd/root.go
- FOUND: internal/cmd/root_test.go
- FOUND: .planning/phases/08-high-availability/08-03-SUMMARY.md
- FOUND commit: 0d39ed1 (test RED phase)
- FOUND commit: 14472f2 (feat GREEN phase)

---
*Phase: 08-high-availability*
*Completed: 2026-03-17*
