---
phase: 03-event-log
plan: 02
subsystem: database
tags: [badger, event-log, cdc, postgres, connector, wal, chk-01, log-01, tdd]

# Dependency graph
requires:
  - phase: 03-01
    provides: EventLog interface (Append / ReadPartition / Close) and BadgerEventLog implementation
  - phase: 02-postgres-source-and-parser
    provides: PostgresConnector struct and receiveLoop with CHK-01 checkpoint ordering

provides:
  - NewWithEventLog constructor: PostgresConnector that accepts an eventlog.EventLog
  - AppendAndQueue method: enforces LOG-01 ordering (Append before channel send before checkpoint)
  - EventLog() accessor: enables testing that eventLog field is non-nil
  - Nil guard: New() (without EventLog) still works; AppendAndQueue skips Append when eventLog is nil

affects:
  - 04-router (router will pass a real BadgerEventLog to NewWithEventLog at startup)
  - 05-ha (HA supervisor wires connector with EventLog during reconnect lifecycle)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - AppendAndQueue as testable encapsulation of LOG-01 ordering: Append → channel send → (later) store.Save
    - Nil guard pattern on EventLog field: backward-compatible extension of constructor without breaking callers
    - Exported accessor (EventLog()) for interface field enabling black-box test assertions

key-files:
  created: []
  modified:
    - internal/source/postgres/connector.go — added eventLog field, NewWithEventLog, EventLog(), AppendAndQueue; wired AppendAndQueue into receiveLoop
    - internal/source/postgres/connector_test.go — added 4 new tests (A/B/C/D) with mockEventLog and mockCheckpointStore

key-decisions:
  - "AppendAndQueue extracted as exported method rather than inline in receiveLoop — enables black-box unit testing of LOG-01 ordering without a live Postgres connection"
  - "Nil guard on eventLog field: New() delegates to NewWithEventLog(nil) — zero callers need to change; Phase 4 wiring just switches to NewWithEventLog"
  - "Append error returns immediately from AppendAndQueue — caller (receiveLoop) propagates as connection error, triggering reconnect; Postgres re-delivers the transaction; BadgerEventLog dedup index skips the duplicate on the second delivery"

patterns-established:
  - "LOG-01 call site pattern: if c.eventLog != nil { if _, err := c.eventLog.Append(ev); err != nil { return fmt.Errorf(\"eventlog: append: %w\", err) } }"
  - "CHK-01 preserved: AppendAndQueue must succeed before the Commit handler calls store.Save — the ordering is maintained because receiveLoop calls AppendAndQueue inside the if ev != nil block, which runs before the if WALData[0] == 'C' Commit block"

requirements-completed: [LOG-01]

# Metrics
duration: 3min
completed: 2026-03-08
---

# Phase 3 Plan 2: PostgresConnector EventLog Wiring Summary

**BadgerEventLog integrated into PostgresConnector via AppendAndQueue — every WAL event is durably stored before the source LSN is acknowledged to Postgres (LOG-01 + CHK-01 ordering enforced)**

## Performance

- **Duration:** ~3 min
- **Started:** 2026-03-08T03:56:15Z
- **Completed:** 2026-03-08T03:59:00Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 2

## Accomplishments

- Added `eventLog eventlog.EventLog` field to `PostgresConnector` struct
- Added `NewWithEventLog` constructor (4th param) and nil-guarded backward-compat `New`
- Added `AppendAndQueue` exported method encapsulating LOG-01 ordering: Append → channel send
- Wired `AppendAndQueue` into `receiveLoop` XLogData handler (replaces bare channel send)
- All 8 tests pass: 4 pre-existing + 4 new (Test A/B/C/D from plan)

## Task Commits

TDD: RED then GREEN:

1. **RED: Failing tests** - `e0be9fb` (test) — 4 new tests for EventLog wiring (all fail at compile)
2. **GREEN: Implementation** - `1fa5b1c` (feat) — connector.go with eventLog field, NewWithEventLog, AppendAndQueue, receiveLoop wiring

## Files Created/Modified

- `internal/source/postgres/connector.go` — Added `eventLog` field, `NewWithEventLog`, `EventLog()`, `AppendAndQueue`; wired into `receiveLoop`
- `internal/source/postgres/connector_test.go` — Added `mockEventLog`, `mockCheckpointStore`, and 4 new tests (A: constructor, B: ordering, C: error propagation, D: nil guard)

## Decisions Made

- **AppendAndQueue as exported method:** The ordering contract (Append before channel send) needed to be testable without a live `*pgconn.PgConn`. Extracting it as an exported method allows black-box tests in `connector_test.go` (external test package) to call it directly with a mock EventLog, verifying ordering and error propagation without any live Postgres dependency.
- **Nil guard via New() delegation:** `New()` now delegates to `NewWithEventLog(cfg, store, idGen, nil)`. This means zero breaking changes for existing callers; Phase 4 simply switches `New` to `NewWithEventLog` with a real BadgerEventLog instance.
- **Append error = connection error:** When `Append` fails, `AppendAndQueue` returns the error, `receiveLoop` propagates it, and the outer `Run` loop triggers exponential backoff reconnect. Postgres re-delivers the transaction from the last acknowledged LSN. The BadgerEventLog's idempotency dedup index will skip the re-delivered duplicate on the second delivery.

## Deviations from Plan

None — plan executed exactly as written. The only transient issue was a disk-full error during test linking (`no space left on device` at 99% disk capacity), resolved by running `go clean -cache -testcache` which freed 536MB. This was a tooling/environment issue, not a code deviation.

## Issues Encountered

- Transient disk-full error during `go test ./internal/...` (`no space left on device` during linker step). Disk was at 99% capacity (140MB free). Running `go clean -cache -testcache -fuzzcache` freed 536MB. All tests passed on retry.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- Phase 3 is complete: BadgerEventLog (03-01) + connector wiring (03-02) both done
- Phase 4 router will call `NewWithEventLog(cfg, store, idGen, badgerLog)` to pass the real event log
- The LOG-01 invariant is now enforced end-to-end: every WAL event is durably written to Badger before `store.Save` advances the checkpoint

---
*Phase: 03-event-log*
*Completed: 2026-03-08*

## Self-Check: PASSED

- FOUND: internal/source/postgres/connector.go
- FOUND: internal/source/postgres/connector_test.go
- FOUND: .planning/phases/03-event-log/03-02-SUMMARY.md
- FOUND commit: e0be9fb (RED: failing tests)
- FOUND commit: 1fa5b1c (GREEN: implementation)
