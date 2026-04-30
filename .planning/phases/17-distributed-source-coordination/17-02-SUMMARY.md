---
phase: 17-distributed-source-coordination
plan: 02
subsystem: source
tags: [postgres, cdc, wal, epoch-fencing, cluster, distributed]

requires:
  - phase: 17-01-distributed-source-coordination
    provides: WalLeaderElector with EpochGetter func() (uint64, bool)
provides:
  - epochGetter field on PostgresConnector for cluster-mode epoch fencing
  - SetEpochGetter injection method following SetBackfillEngine pattern
  - ShouldSendStandby exported helper for testable epoch guard logic
  - Fenced sendStandbyStatus that drops standby updates when not WAL leader
affects:
  - internal/cmd/root.go (Plan 17-03 will call SetEpochGetter on connector)
  - internal/cluster/wal_leader.go (provides the EpochGetter func)

tech-stack:
  added: []
  patterns:
    - "Optional injection pattern: SetEpochGetter mirrors SetBackfillEngine — field set once before Run, never mutated during Run, nil means non-cluster mode"
    - "Exported testable helper: ShouldSendStandby isolates guard logic from pglogrepl network dependency so unit tests need no live Postgres"
    - "Zombie node fence: drop standby (not cancel ctx) so wal_receiver_timeout closes connection naturally without corrupting in-flight events"

key-files:
  created: []
  modified:
    - internal/source/postgres/connector.go
    - internal/source/postgres/connector_test.go

key-decisions:
  - "ShouldSendStandby exported (not unexported shouldSendStandby) so test package (postgres_test) can test it directly without reflection or build tags"
  - "epochGetter func pointer set once by SetEpochGetter before Run starts — never mutated during Run — so no mutex is needed in the connector (WalLeaderElector reads its own atomic.Bool internally)"
  - "Zombie node drops standby update (returns nil) rather than cancelling ctx — context cancellation closes the replication slot which can corrupt in-flight events; wal_receiver_timeout (~60s) is the correct fence"
  - "nil epochGetter path is byte-for-byte identical to pre-Phase-17: the ShouldSendStandby(nil) fast path returns true unconditionally"

patterns-established:
  - "Epoch fence guard: if !ShouldSendStandby(c.epochGetter) { return nil } at top of sendStandbyStatus — clean early return before any network call"

requirements-completed:
  - SRCC-01

duration: 4min
completed: 2026-04-30
---

# Phase 17 Plan 02: Epoch-Fenced sendStandbyStatus Summary

**Epoch fencing for PostgresConnector.sendStandbyStatus via optional epochGetter injection — zombie WAL node silently drops standby updates when not WAL leader (SRCC-01)**

## Performance

- **Duration:** ~4 min
- **Started:** 2026-04-30T16:54:31Z
- **Completed:** 2026-04-30T16:57:51Z
- **Tasks:** 1 (TDD: 2 commits — RED + GREEN)
- **Files modified:** 2

## Accomplishments

- Added `epochGetter func() (uint64, bool)` field to `PostgresConnector` struct
- Added `SetEpochGetter` setter following the `SetBackfillEngine` optional-injection pattern
- Exported `ShouldSendStandby` helper isolates guard logic for unit tests without live Postgres
- Modified `sendStandbyStatus` to call `ShouldSendStandby(c.epochGetter)` — nil getter path unchanged
- 19/19 tests pass including all 6 new epoch fencing tests; `make verify-no-cgo` passes

## Task Commits

Each task was committed atomically (TDD pattern — two commits):

1. **RED — Failing epoch fencing tests** - `307e86d` (test)
2. **GREEN — Epoch fencing implementation** - `f6cd282` (feat)

_TDD task: test commit followed by implementation commit_

## Files Created/Modified

- `internal/source/postgres/connector.go` — epochGetter field, SetEpochGetter, ShouldSendStandby, fenced sendStandbyStatus
- `internal/source/postgres/connector_test.go` — 6 new epoch fencing tests (TestShouldSendStandby_*, TestSetEpochGetter_*)

## Decisions Made

- `ShouldSendStandby` is exported so the `postgres_test` package can test it directly without reflection or build tags — internal callers use `sendStandbyStatus` which calls the helper
- `epochGetter` func pointer is set once before `Run` starts, never mutated during `Run`, so no mutex is needed in the connector (`WalLeaderElector` reads its own `atomic.Bool` internally)
- Zombie node drops the standby update (`return nil`) rather than cancelling ctx — context cancellation would close the replication slot which can corrupt in-flight events; Postgres's `wal_receiver_timeout` (~60s default) is the correct fence mechanism
- `nil epochGetter` path is byte-for-byte identical to pre-Phase-17: `ShouldSendStandby(nil)` returns `true` unconditionally — non-cluster deployments completely unaffected

## Deviations from Plan

None — plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- `SetEpochGetter` is ready for Plan 17-03 (`root.go` wiring) to call `connector.SetEpochGetter(walLeader.EpochGetter)` in cluster mode
- `ShouldSendStandby` can be called directly in any future test that needs to verify fencing behavior
- All pre-existing postgres connector tests continue to pass — no regressions

## Self-Check

### Files exist

- `internal/source/postgres/connector.go` — modified with epochGetter, SetEpochGetter, ShouldSendStandby, fenced sendStandbyStatus
- `internal/source/postgres/connector_test.go` — modified with 6 new epoch fencing tests

### Commits exist

- `307e86d` — test(17-02): add failing tests for epoch fencing (SRCC-01)
- `f6cd282` — feat(17-02): add epochGetter field, SetEpochGetter, and fenced sendStandbyStatus

## Self-Check: PASSED

---
*Phase: 17-distributed-source-coordination*
*Completed: 2026-04-30*
