---
phase: 16-partition-ownership-and-active-active-delivery
plan: "01"
subsystem: database
tags: [postgres, pgx, cluster, partitions, cdc, ownership]

# Dependency graph
requires:
  - phase: 14-shared-state-foundation
    provides: PostgresCursorStore and cluster membership patterns (pgx.Conn + SQL const style)
provides:
  - PartitionStore struct with atomic claim/steal/release over kaptanto_partitions table
  - OpenPartitionStore: creates and seeds 64-row kaptanto_partitions table in Postgres
  - ClaimUnclaimed: race-free atomic partition claiming via UPDATE WHERE owner_node_id IS NULL RETURNING
  - StealStalePartitions: single-round-trip takeover of all partitions from a stale node
  - ReleaseAll: graceful release of all partitions on shutdown
  - ListOwned: queries live ownership state, always returns non-nil slice
  - EpochFor: thread-safe in-memory epoch lookup for a given partition
affects:
  - 16-02 (PartitionManager builds directly on PartitionStore)
  - 16-03 (epochCursorStore wraps ConsumerCursorStore + epoch guard using EpochFor)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "SQL constants as package-level const strings (same as membership.go)"
    - "pgx.ErrNoRows as race-loss sentinel — not an error, silent skip in ClaimUnclaimed"
    - "Non-nil empty slice invariant for all ownership query return values"
    - "sync.RWMutex over in-memory epochs map for thread-safe epoch reads"

key-files:
  created:
    - internal/cluster/partitions.go
    - internal/cluster/partitions_test.go
  modified: []

key-decisions:
  - "pgx.ErrNoRows from ClaimUnclaimed UPDATE RETURNING is treated as normal race loss — skipped silently, not surfaced as error"
  - "Non-nil empty slice invariant applies to ClaimUnclaimed, StealStalePartitions, and ListOwned — matching StaleNodes contract from NodeHeartbeater"
  - "EpochFor reads from in-memory epochs map under RLock — avoids DB round-trip for hot path partition validation in Plan 03"
  - "OpenPartitionStore idempotently seeds 64 rows via INSERT ... ON CONFLICT DO NOTHING — safe to call on every node startup"

patterns-established:
  - "PartitionStore SQL const style: package-level const strings at top of file, matching membership.go"
  - "Ownership methods: all return non-nil slices with explicitly initialized []PartitionClaim{} literals"
  - "Race-safe claim: SELECT unclaimed first, then per-row UPDATE WHERE owner_node_id IS NULL RETURNING — each row atomically won by exactly one node"

requirements-completed: [DLVR-01, DLVR-02]

# Metrics
duration: 3min
completed: 2026-04-30
---

# Phase 16 Plan 01: PartitionStore Summary

**Race-free atomic partition ownership over Postgres kaptanto_partitions table with claim, steal, release, and epoch tracking**

## Performance

- **Duration:** 3 min
- **Started:** 2026-04-30T00:14:33Z
- **Completed:** 2026-04-30T00:17:30Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments

- PartitionStore with OpenPartitionStore, ClaimUnclaimed, StealStalePartitions, ReleaseAll, ListOwned, EpochFor, and Close
- kaptanto_partitions schema (64 fixed rows, partition_id/owner_node_id/epoch/claimed_at) auto-created and seeded on Open
- 7 unit tests covering SQL constant patterns and EpochFor paths — all pass under CGO_ENABLED=0 without a Postgres connection

## Task Commits

Each task was committed atomically:

1. **Task 1: Create PartitionStore (GREEN)** - `8da7aaa` (feat)
2. **Task 2: Write PartitionStore unit tests** - `236041b` (test)

_Note: TDD RED phase produced failing tests first; GREEN phase produced the implementation._

## Files Created/Modified

- `internal/cluster/partitions.go` - PartitionStore struct, SQL constants, all ownership operations
- `internal/cluster/partitions_test.go` - 7 unit tests for SQL patterns and EpochFor in-memory paths

## Decisions Made

- pgx.ErrNoRows from ClaimUnclaimed UPDATE RETURNING treated as normal race loss — silent skip, not an error
- Non-nil empty slice invariant applied consistently to all three query-returning methods
- EpochFor reads from in-memory epochs map under RLock — avoids DB round-trip for Plan 03 hot-path use
- Table seed uses INSERT ON CONFLICT DO NOTHING — idempotent across multi-node concurrent starts

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None

## User Setup Required

None - no external service configuration required. kaptanto_partitions table is auto-created by OpenPartitionStore at startup.

## Next Phase Readiness

- PartitionStore is importable by Plan 02's PartitionManager (OpenPartitionStore, ClaimUnclaimed, StealStalePartitions, ReleaseAll, ListOwned, EpochFor all exported)
- Plan 03's epochCursorStore can use EpochFor for hot-path epoch validation without DB round-trips
- `make test`, `make build`, `make verify-no-cgo` all pass

---
*Phase: 16-partition-ownership-and-active-active-delivery*
*Completed: 2026-04-30*
