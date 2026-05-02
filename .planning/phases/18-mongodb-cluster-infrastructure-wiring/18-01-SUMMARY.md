---
phase: 18-mongodb-cluster-infrastructure-wiring
plan: 01
subsystem: infra
tags: [cluster, mongodb, partition-manager, heartbeater, cdc]

# Dependency graph
requires:
  - phase: 17-wal-leader-elector
    provides: WalLeaderElector and cluster goroutine wiring pattern in runPipeline (Postgres path)
  - phase: 16-partition-ownership-and-active-active-delivery
    provides: PartitionManager, NodeHeartbeater, EpochCursorStore
provides:
  - runMongoPipeline with 11-parameter signature accepting heartbeater and pm
  - heartbeater.Run and pm.Run goroutines launched in both g and g2 errgroup blocks for MongoDB+cluster
  - deferred pm.ReleaseAll at function entry covering all MongoDB pipeline return paths
affects:
  - 18-02 (next plan in same phase)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "cluster goroutine wiring inside MongoDB errgroup blocks mirrors Postgres pattern"
    - "deferred pm.ReleaseAll with context.Background() fires on any return path including panic recovery"
    - "nil guard (if pm != nil) ensures non-cluster MongoDB pipelines are byte-for-byte identical to pre-Phase-18"

key-files:
  created: []
  modified:
    - internal/cmd/root.go

key-decisions:
  - "heartbeater and pm passed as explicit parameters to runMongoPipeline (nil when !cfg.Cluster) — mirrors Postgres pattern, no global state"
  - "Single deferred pm.ReleaseAll at function entry — NOT after g.Wait() or g2.Wait() — covers all return paths"
  - "walElector NOT passed to runMongoPipeline — no WAL source coordination needed for MongoDB"
  - "pm.Run restarts in g2 because it was cancelled when g1 ended — pm.owned persists across both errgroup blocks (shared pointer)"

patterns-established:
  - "MongoDB errgroup cluster block mirrors Postgres errgroup cluster block (heartbeater.Run + pm.Run, no walElector)"

requirements-completed: [STATE-02, DLVR-01, DLVR-02, DLVR-03]

# Metrics
duration: 5min
completed: 2026-05-02
---

# Phase 18 Plan 01: MongoDB Cluster Infrastructure Wiring Summary

**runMongoPipeline wired with heartbeater.Run + pm.Run goroutines and deferred pm.ReleaseAll, fixing STATE-02 and DLVR-02/DLVR-03 for MongoDB+cluster deployments**

## Performance

- **Duration:** ~5 min
- **Started:** 2026-05-02T13:51:00Z
- **Completed:** 2026-05-02T13:56:55Z
- **Tasks:** 2
- **Files modified:** 1

## Accomplishments
- Updated `runMongoPipeline` signature from 9 to 11 parameters (added `heartbeater *cluster.NodeHeartbeater` and `pm *cluster.PartitionManager`)
- Updated call site in `runPipeline` to pass both new arguments (already in scope at call site)
- Wired `heartbeater.Run` and `pm.Run` in both errgroup blocks (`g` and `g2`) under `if cfg.Cluster` guard
- Added single deferred `pm.ReleaseAll` at function entry guarded by `if pm != nil` — fires on any return path
- Non-cluster MongoDB pipelines are byte-for-byte identical to pre-Phase-18 behavior

## Task Commits

Each task was committed atomically:

1. **Task 1: Update runMongoPipeline signature and call site** - `fd69149` (feat)
2. **Task 2: Wire cluster goroutines and ReleaseAll in runMongoPipeline** - `a79e668` (feat)

**Plan metadata:** _(docs commit follows)_

## Files Created/Modified
- `internal/cmd/root.go` - Updated runMongoPipeline signature, call site, and function body with cluster wiring

## Decisions Made
- `heartbeater` and `pm` passed as explicit nil-able parameters rather than accessing them via closure — consistent with how the Postgres path works and avoids capturing outer scope mutations
- Single deferred `pm.ReleaseAll` at function entry (not after each `g.Wait()`) — ensures exactly one release call on any return path, including both normal shutdown and the re-snapshot path
- `walElector` NOT passed to `runMongoPipeline` — MongoDB does not need WAL source coordination; walElector is inherently a Postgres concept (epoch-fencing standby status updates)
- `pm.Run` restarts inside `g2` because the g1 context was cancelled; `pm.owned` survives the gap between errgroups since it is maintained by the shared `pm` pointer

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- Phase 18 Plan 01 complete; `runMongoPipeline` now starts cluster infrastructure goroutines for MongoDB+cluster deployments
- STATE-02 (kaptanto_nodes row for MongoDB nodes) and DLVR-03 (cursor saves not silently dropped) are now functional
- Ready for Phase 18 Plan 02

---
*Phase: 18-mongodb-cluster-infrastructure-wiring*
*Completed: 2026-05-02*
