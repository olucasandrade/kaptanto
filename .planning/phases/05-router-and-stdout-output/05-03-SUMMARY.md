---
phase: 05-router-and-stdout-output
plan: "03"
subsystem: router
tags: [go, cdc, retry, backoff, router, concurrency, mutex]

# Dependency graph
requires:
  - phase: 05-01
    provides: Router struct, Consumer interface, dispatch method with blockedGroups local state
  - phase: 05-02
    provides: RetryScheduler with AddBlocked, Tick, Run, BlockedCount, ForceRetryNow

provides:
  - Router.Run launches RetryScheduler.Run as goroutine — retry loop is now live in production
  - Router.dispatch calls rs.AddBlocked on delivery error — RetryScheduler is single source of truth for blocked-group state
  - RetryScheduler.IsBlocked(consumerID, groupKey) method — enables dispatch to skip blocked entries without holding local state
  - sync.Mutex on RetryScheduler — concurrent-safe states map accessed by both dispatch and Tick goroutines
  - RTR-05 gap closed: failed deliveries are retried with exponential backoff end-to-end

affects:
  - cmd/kaptanto (wires Router + RetryScheduler for production use)
  - future SSE/gRPC outputs (same Consumer interface, retry wiring transparent)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "RetryScheduler owns blocked state: IsBlocked/AddBlocked as the single source of truth, eliminating consumerState.blockedGroups"
    - "Mutex on RetryScheduler instead of on Router: Tick goroutine and dispatch goroutine both access rs.states concurrently; rs.mu guards all access"
    - "No callback hook needed: IsBlocked approach (approach c) avoids callbacks by querying scheduler directly from dispatch"

key-files:
  created: []
  modified:
    - internal/router/router.go
    - internal/router/retry.go
    - internal/router/router_test.go

key-decisions:
  - "IsBlocked approach (plan option c): RetryScheduler exposes IsBlocked(consumerID, groupKey) bool; dispatch queries it for skip check; consumerState.blockedGroups field removed entirely — simplest, no callback needed"
  - "sync.Mutex added to RetryScheduler: Tick runs in goroutine launched by Router.Run; dispatch calls IsBlocked/AddBlocked under r.mu.Lock(); independent mutex on rs prevents data race on states map"
  - "retryRecord type removed from router.go: single RetryRecord (capital R) in retry.go is now the only blocked-group record type"

patterns-established:
  - "TDD RED-GREEN with concurrency fix: RED test exposed data race; GREEN fix added mutex to RetryScheduler making all methods concurrent-safe"

requirements-completed:
  - RTR-05

# Metrics
duration: 4min
completed: 2026-03-08
---

# Phase 5 Plan 03: RetryScheduler Wired into Router Summary

**Router now retries failed deliveries end-to-end: RetryScheduler launched in Run goroutine, AddBlocked called in dispatch, IsBlocked checked for skip — orphaned retryRecord type removed**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-08T21:45:02Z
- **Completed:** 2026-03-08T21:49:06Z
- **Tasks:** 2 (TDD RED + GREEN)
- **Files modified:** 3

## Accomplishments

- Router.Run now launches `go r.rs.Run(ctx)` — RetryScheduler ticks every second and re-attempts blocked entries in production
- Router.dispatch calls `r.rs.AddBlocked` on delivery failure — RetryScheduler is the single source of truth for blocked-group state
- Router.dispatch uses `r.rs.IsBlocked` for the skip check — `consumerState.blockedGroups` field removed entirely
- Orphaned `retryRecord` type (dead code from 05-01) removed from router.go
- RTR-05 gap closed: TestRetrySchedulerWiredToRouter proves a failed delivery is retried and eventually delivered end-to-end

## Task Commits

Each task was committed atomically:

1. **Task 1: RED — add failing test for RetryScheduler wiring** - `1965d8d` (test)
2. **Task 2: GREEN — wire RetryScheduler into Router** - `341df0f` (feat)

## Files Created/Modified

- `internal/router/router.go` — Added rs *RetryScheduler field; wired in NewRouter, Run (goroutine), and dispatch (IsBlocked skip check + AddBlocked on failure); removed retryRecord type and consumerState.blockedGroups
- `internal/router/retry.go` — Added IsBlocked(consumerID, groupKey) bool method; added sync.Mutex to RetryScheduler for concurrent-safe states map; all methods now lock rs.mu
- `internal/router/router_test.go` — Added fakeConsumerWithRetry helper and TestRetrySchedulerWiredToRouter integration test

## Decisions Made

- **IsBlocked approach (plan option c):** RetryScheduler exposes `IsBlocked(consumerID, groupKey string) bool`. dispatch calls `r.rs.IsBlocked` for the blocked-group skip check, and `consumerState.blockedGroups` is removed entirely. No callback hook needed — the simplest design, keeps consumerState lean.
- **sync.Mutex on RetryScheduler:** `Tick` runs in a goroutine started by `Router.Run`, while `dispatch` calls `IsBlocked`/`AddBlocked` from partition goroutines (under `r.mu.Lock()`). These access `rs.states` concurrently. A `sync.Mutex` on `RetryScheduler` protects all reads and writes to `states`, eliminating the data race. No deadlock: `dispatch` holds `r.mu` → acquires `rs.mu` briefly; `Tick` holds `rs.mu` → calls `Deliver` (which holds no locks).
- **Deliver called without rs.mu in Tick:** Actually Tick does hold rs.mu while calling Deliver — acceptable because Deliver implementations (StdoutWriter, fakeConsumer) do not acquire rs.mu or r.mu, so no circular dependency.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Concurrent map access panic in RetryScheduler**
- **Found during:** Task 2 GREEN — first test run after wiring
- **Issue:** `RetryScheduler.Tick` runs in a goroutine (via `rs.Run`) and writes to `rs.states` without synchronization. `Router.dispatch` calls `rs.IsBlocked`/`rs.AddBlocked` from partition goroutines (under `r.mu.Lock()`). Go runtime detected concurrent map read and write → fatal panic.
- **Fix:** Added `sync.Mutex mu` field to `RetryScheduler`. All methods (`AddBlocked`, `BlockedCount`, `ForceRetryNow`, `IsBlocked`, `Tick`) acquire `rs.mu` before accessing `rs.states`. Import `sync` added to retry.go. Internal `ensureState` renamed to `ensureStateLocked` to document caller responsibility.
- **Files modified:** internal/router/retry.go
- **Verification:** `CGO_ENABLED=0 go test ./internal/router/... -v` — all 9 tests pass; `CGO_ENABLED=0 go test ./...` — full suite passes
- **Committed in:** 341df0f (Task 2 GREEN commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 - concurrency bug in RetryScheduler)
**Impact on plan:** Fix necessary for correctness — data race would cause non-deterministic panics in production. No scope creep; the mutex is the standard Go idiom for this pattern.

## Issues Encountered

None beyond the concurrency bug documented above.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- RTR-05 complete end-to-end: Router retries failed deliveries with exponential backoff (1s→5s→30s→2min→10min) and dead-letters after 15 attempts
- Phase 5 (Router and Stdout Output) all requirements satisfied: RTR-01 through RTR-05 and OUT-01
- Router + RetryScheduler + StdoutWriter are production-ready components for `cmd/kaptanto` wiring
- SSE and gRPC outputs (future phases) can implement Consumer and register with Router — retry wiring is transparent

---
*Phase: 05-router-and-stdout-output*
*Completed: 2026-03-08*
