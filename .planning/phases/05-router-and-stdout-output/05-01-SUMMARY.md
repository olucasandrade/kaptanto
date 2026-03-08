---
phase: 05-router-and-stdout-output
plan: "01"
subsystem: router
tags: [go, cdc, eventlog, consumer, partition, goroutine, message-group-blocking]

# Dependency graph
requires:
  - phase: 03-event-log
    provides: EventLog interface and LogEntry type consumed by Router.runPartition via ReadPartition
  - phase: 03-event-log
    provides: PartitionOf function for consistent partition hashing at write and read path

provides:
  - Consumer interface exported from internal/router (ID() string + Deliver(...) error)
  - ConsumerCursorStore interface exported from internal/router
  - NewNoopCursorStore: in-memory cursor store returning 1 for unknown cursors
  - Router struct with NewRouter constructor, Register, and Run methods
  - Per-partition goroutine poll loop with 10ms sleep on empty batch
  - Per-consumer blockedGroups map enforcing per-key delivery ordering (RTR-04)

affects:
  - 05-02-stdout-output (implements Consumer for stdout delivery)
  - 05-03-sse-output (implements Consumer for SSE delivery)
  - future-grpc-output (implements Consumer for gRPC delivery)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Cursor semantics: store next-to-read seq (not last-delivered), so cursor=1 on startup means read from seq 1 and after delivering seq N the cursor becomes N+1"
    - "Per-consumer blockedGroups map[string]*retryRecord: key is string(entry.Event.Key), failed delivery blocks only subsequent events for that key"
    - "minCursorForPartition returns min across all consumers so no consumer misses an event even if one is ahead"
    - "Router.Run starts exactly numPartitions goroutines via sync.WaitGroup, returns nil on ctx cancel"

key-files:
  created:
    - internal/router/router.go
    - internal/router/router_test.go
  modified: []

key-decisions:
  - "Cursor stores next-to-read seq (not last-delivered seq) — eliminates ambiguity between initial state (cursor=1) and post-delivery state; after delivering seq N cursor becomes N+1"
  - "NewNoopCursorStore exported as constructor — enables tests to directly verify LoadCursor=1 invariant without going through NewRouter internals"
  - "dispatch holds mu.Lock for entire fan-out — keeps blockedGroups and cursorByPartition mutations serialized without per-consumer locks; acceptable because delivery is fast (consumers do I/O in their own goroutines if needed)"
  - "runPartition never returns on ReadPartition error — logs and retries with pollInterval sleep; only ctx.Done() exits the goroutine (RTR-02)"

patterns-established:
  - "Consumer interface: two-method interface (ID + Deliver) that stdout, SSE, gRPC all implement"
  - "Per-key poison-pill isolation: blockedGroups map prevents a single bad key from stalling the entire partition"

requirements-completed:
  - RTR-01
  - RTR-02
  - RTR-03
  - RTR-04

# Metrics
duration: 4min
completed: 2026-03-08
---

# Phase 5 Plan 01: Router Core Summary

**Fan-out router with Consumer/ConsumerCursorStore interfaces, per-partition goroutines, and per-key message-group blocking using an in-memory blockedGroups map**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-08T20:59:57Z
- **Completed:** 2026-03-08T21:03:53Z
- **Tasks:** 2 (TDD RED + GREEN)
- **Files modified:** 2

## Accomplishments

- Consumer and ConsumerCursorStore interfaces exported from `internal/router`, ready for stdout/SSE/gRPC implementors
- Router.Run starts exactly numPartitions goroutines; all exit cleanly on context cancel (RTR-02)
- Per-key poison-pill isolation verified: key "B" events flow unaffected when key "A" delivery fails (RTR-04)
- noopCursorStore returns 1 for unknown cursors, preventing seq=0 (dedup sentinel) from being used as a start position (RTR-03)

## Task Commits

Each task was committed atomically:

1. **Task 1: RED — failing tests for router core RTR-01 RTR-02 RTR-03 RTR-04** - `c2af73a` (test)
2. **Task 2: GREEN — implement router core** - `f9eec75` (feat)

_Note: TDD tasks have two commits (test RED then feat GREEN). A one-line fix was applied during GREEN (cursor semantics) — counted as part of Task 2._

## Files Created/Modified

- `internal/router/router.go` — Router struct, Consumer interface, ConsumerCursorStore interface, noopCursorStore, partition goroutines, message group blocking via blockedGroups
- `internal/router/router_test.go` — 5 tests covering RTR-01 through RTR-04 behaviors

## Decisions Made

- **Cursor stores next-to-read seq, not last-delivered:** Initial value 1 = "read from seq 1". After delivering seq N, cursor becomes N+1. This eliminates re-delivery of the same entry on every poll loop iteration.
- **NewNoopCursorStore exported:** Enables TestLoadCursorDefaultIsOne to directly verify the LoadCursor=1 invariant without relying on Router internals.
- **dispatch serialized under mu.Lock:** Fan-out to all consumers is serialized per-entry. Acceptable because Deliver is expected to be fast (no blocking I/O inside); consumers that need async I/O do it in their own goroutines.
- **runPartition never returns on error:** Logs warning and retries after pollInterval. Only ctx.Done() exits the goroutine, matching RTR-02.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Cursor semantics corrected to prevent infinite re-delivery**
- **Found during:** Task 2 (GREEN — implement router.go)
- **Issue:** Initial implementation stored `entry.Seq` (last delivered) as the cursor. After delivering seq=1, cursor=1, so next ReadPartition call used fromSeq=1 again — re-delivering seq=1 indefinitely. TestRouterDeliversSingleEvent received 1,283,332 entries instead of 1.
- **Fix:** Changed cursor semantics to store `entry.Seq + 1` (next to read). After delivering seq=1, cursor=2, so next ReadPartition uses fromSeq=2 and gets an empty batch.
- **Files modified:** internal/router/router.go
- **Verification:** TestRouterDeliversSingleEvent passes with exactly 1 delivered entry
- **Committed in:** f9eec75 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 - bug in cursor advancement logic)
**Impact on plan:** Fix necessary for correctness — infinite re-delivery would break all consumers. No scope creep.

## Issues Encountered

None beyond the cursor semantics bug documented above.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Consumer and ConsumerCursorStore interfaces are ready for 05-02 (stdout output implementation)
- Router.Register and Router.Run are tested and production-ready
- noopCursorStore handles stdout phase (no persistence needed); 05-02 will add SQLiteCursorStore for durable cursor state

---
*Phase: 05-router-and-stdout-output*
*Completed: 2026-03-08*
