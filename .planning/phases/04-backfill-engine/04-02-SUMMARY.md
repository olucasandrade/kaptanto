---
phase: 04-backfill-engine
plan: 02
subsystem: backfill
tags: [backfill, connector, pgx, goroutine, mutex, snapshot-loop, tdd]
dependency_graph:
  requires:
    - internal/backfill/backfill.go (BackfillEngine interface, BackfillConfig, BackfillState, MakeReadEvent, MakeControlEvent)
    - internal/backfill/cursor.go (KeysetCursor, BuildFirstQuery, BuildNextQuery)
    - internal/backfill/watermark.go (WatermarkChecker, ShouldEmit)
    - internal/backfill/optimizer.go (BatchOptimizer)
    - internal/source/postgres/connector.go (PostgresConnector, AppendAndQueue, NewWithEventLog)
    - jackc/pgx/v5 (pgx.Conn for snapshot queries)
  provides:
    - internal/backfill/backfill.go (BackfillEngineImpl, NewBackfillEngine, AppendFn, OpenConnFn)
    - internal/source/postgres/connector.go (NewWithBackfill constructor, backfill goroutine launch, appendMu serialization)
  affects:
    - internal/backfill/backfill_test.go (3 new BackfillEngineImpl tests)
    - internal/source/postgres/connector_test.go (3 new NewWithBackfill tests)
tech_stack:
  added:
    - sync.Mutex (appendMu in PostgresConnector — serializes concurrent Append calls)
  patterns:
    - TDD (RED commit then GREEN commit for Task 2)
    - Dependency injection via AppendFn/OpenConnFn callbacks
    - Keyset cursor pagination with crash-resumable state
    - Goroutine launch guarded by HasPendingBackfills() + nil check
    - Mutex serialization for concurrent Append from WAL + backfill goroutines
key_files:
  created: []
  modified:
    - internal/backfill/backfill.go (BackfillEngineImpl, full snapshotTable loop)
    - internal/backfill/backfill_test.go (3 new BackfillEngineImpl integration tests)
    - internal/source/postgres/connector.go (NewWithBackfill, appendMu, backfill goroutine)
    - internal/source/postgres/connector_test.go (3 new NewWithBackfill tests + mockBackfillEngine)
decisions:
  - "BackfillEngineImpl coexists with engine struct — NewEngine (Plan 01 tests) and NewBackfillEngine (production with AppendFn/OpenConnFn) are separate constructors; no test breakage"
  - "appendMu sync.Mutex added to PostgresConnector — serializes concurrent eventLog.Append calls from WAL receiveLoop and backfill goroutine without restructuring AppendAndQueue"
  - "Backfill goroutine launched after StartReplication and before WAL lag monitor — correct ordering ensures slot exists before snapshot reads begin"
  - "WatermarkChecker is optional field on BackfillEngineImpl (SetWatermark) — nil disables watermark deduplication; production wiring sets it via SetWatermark after construction"
  - "snapshotID generated once per snapshotTable call using UnixNano — stable within a single run, unique across restarts"
metrics:
  duration: "3 min"
  completed: "2026-03-08"
  tasks_completed: 2
  files_created: 0
  files_modified: 4
---

# Phase 4 Plan 2: Backfill Engine Wiring Summary

**One-liner:** BackfillEngineImpl with full keyset-cursor snapshot loop wired into PostgresConnector via AppendFn/OpenConnFn callbacks, serialized by appendMu, launched as goroutine after StartReplication.

## What Was Built

### Task 1: BackfillEngineImpl with pgx.Conn callback and AppendFn integration

Added `BackfillEngineImpl` to `internal/backfill/backfill.go`:

- **AppendFn / OpenConnFn** type aliases for dependency injection — production uses `connector.AppendAndQueue` and `pgx.Connect`; tests use mocks.
- **NewBackfillEngine** constructor accepting configs, store, idGen, appendFn, openConnFn.
- **HasPendingBackfills()** — reads state from store per config, returns true if any is pending/running; logs on error and returns false (conservative).
- **Run(ctx)** — iterates configs, calling `runOne` for each. On completion emits `snapshot_complete` control event and saves completed state.
- **snapshotTable(ctx, cfg, state)** — full keyset-cursor snapshot loop:
  - Opens pgx.Conn via `openConnFn` (never the replication connection).
  - Estimates total rows from `pg_class.reltuples`.
  - Restores cursor position from `state.CursorKey` on crash resume.
  - Loops: builds first/next query, executes via pgx, scans rows via `rows.Values()`.
  - Applies watermark check (if set) to skip superseded rows.
  - Calls `appendFn` for each emitted read event.
  - Saves state after each batch (BKF-03 crash-resumable).
  - Uses `BatchOptimizer.Adjust` for adaptive batch sizing.
  - Breaks when batch returns fewer rows than batch size.

3 new unit tests for `BackfillEngineImpl` with mock AppendFn and OpenConnFn.

### Task 2: NewWithBackfill constructor and goroutine launch in connector (TDD)

Added to `internal/source/postgres/connector.go`:

- **backfillEng backfill.BackfillEngine** field on `PostgresConnector`.
- **appendMu sync.Mutex** field — serializes `eventLog.Append` calls from concurrent WAL and backfill goroutines (prevents BadgerDB write races — Pitfall 2).
- **NewWithBackfill(cfg, store, idGen, el, bf)** — delegates to `NewWithEventLog`, sets `backfillEng`. Nil bf is handled gracefully.
- **Backfill goroutine launch** in `connectAndStream`, after `StartReplication` succeeds:
  ```go
  if c.backfillEng != nil && c.backfillEng.HasPendingBackfills() {
      go func() { ... c.backfillEng.Run(ctx) ... }()
  }
  ```
- **AppendAndQueue** updated to lock `appendMu` around `eventLog.Append`.

3 new TDD tests: `TestNewWithBackfill_StoresEngine`, `TestNewWithBackfill_NilBackfill`, `TestNewWithBackfill_ExistingConstructorsUnchanged`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Added mutex serialization for concurrent Append**
- **Found during:** Task 2 implementation review
- **Issue:** Plan noted "if Append is called directly (not via channel), add a sync.Mutex". AppendAndQueue calls `eventLog.Append` directly; WAL goroutine and backfill goroutine both call `AppendAndQueue` concurrently. Without mutex, concurrent BadgerDB writes would race.
- **Fix:** Added `appendMu sync.Mutex` field and locked around `Append` in `AppendAndQueue`.
- **Files modified:** `internal/source/postgres/connector.go`
- **Commit:** `09c26de`

## Self-Check: PASSED

| Item | Status |
|------|--------|
| internal/backfill/backfill.go — BackfillEngineImpl | FOUND |
| internal/backfill/backfill_test.go — 3 new tests | FOUND |
| internal/source/postgres/connector.go — NewWithBackfill | FOUND |
| internal/source/postgres/connector_test.go — 3 new tests | FOUND |
| Task 1 commit 8e8075a | FOUND |
| Task 2 RED commit b996a5a | FOUND |
| Task 2 GREEN commit 09c26de | FOUND |
| CGO_ENABLED=0 go build ./... | PASSED |
| CGO_ENABLED=0 go test ./internal/... | PASSED (8 packages) |
