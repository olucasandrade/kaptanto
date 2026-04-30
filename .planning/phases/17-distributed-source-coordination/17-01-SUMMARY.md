---
phase: 17-distributed-source-coordination
plan: 01
subsystem: infra
tags: [nats, jetstream, kv, leader-election, cluster, atomic, go]

# Dependency graph
requires:
  - phase: 15-nats-event-log
    provides: NatsEventLog with embedded NATS server and *nats.Conn field
  - phase: 16-partition-ownership-and-active-active-delivery
    provides: cluster package, PartitionManager, epochCursorStore

provides:
  - WalLeaderElector: NATS JetStream KV TTL lease-based WAL leader election
  - NatsEventLog.Conn() accessor for *nats.Conn reuse by cluster components
  - EpochGetter() returning (revision, isLeader) atomically

affects:
  - 17-02 (root.go wiring of WalLeaderElector)
  - internal/cmd/root.go (needs natsEl.Conn() and WalLeaderElector.Run)
  - internal/source/postgres/connector.go (SetEpochGetter consumer)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - NATS JetStream KV TTL lease pattern for distributed leader election
    - atomic.Uint64/atomic.Bool for lock-free hot-path EpochGetter reads
    - Idempotent bucket open-or-create: try KeyValue first, CreateKeyValue on miss
    - TDD RED/GREEN with in-process natstest.RunServer for cluster unit tests

key-files:
  created:
    - internal/cluster/wal_leader.go
    - internal/cluster/wal_leader_test.go
  modified:
    - internal/eventlog/nats.go

key-decisions:
  - "WalLeaderElector uses kv.Create for atomic acquisition and kv.Update CAS for renewal — never kv.Get to verify own lease (avoids stale read pitfall)"
  - "leaseTTL=15s is 2x renewEvery=7s — one missed renewal heartbeat does not evict the leader prematurely"
  - "epoch and isLeader stored in atomic.Uint64/Bool — no mutex on hot EpochGetter read path"
  - "Connector context is never cancelled from WalLeaderElector — only isLeader=false is set, WAL replication connection continues cleanly"
  - "openOrCreateLeaderBucket: try KeyValue first (fast path for restarts), CreateKeyValue only on miss, retry open on race condition"
  - "watchForExpiry handles KeyValueDelete and KeyValuePurge operations for correct TTL expiry detection"

patterns-established:
  - "NATS KV lease pattern: kv.Create → holdLease (ticker + CAS kv.Update) → watchForExpiry on failure"
  - "Cluster test helper startTestNATSForCluster uses natstest.RunServer with JetStream=true and t.TempDir() for isolation"

requirements-completed:
  - SRCC-02

# Metrics
duration: 18min
completed: 2026-04-30
---

# Phase 17 Plan 01: WalLeaderElector with NATS JetStream KV TTL Lease Summary

**WalLeaderElector acquires WAL_LEADER_LEASE via atomic kv.Create and renews every 7s via CAS kv.Update, with epoch and isLeader in atomic fields for lock-free EpochGetter reads**

## Performance

- **Duration:** 18 min
- **Started:** 2026-04-30T16:40:00Z
- **Completed:** 2026-04-30T16:58:33Z
- **Tasks:** 2 (+ TDD RED commit)
- **Files modified:** 3

## Accomplishments
- Added `Conn() *nats.Conn` accessor to `NatsEventLog` so the elector can reuse the existing NATS connection without opening a second client
- Implemented `WalLeaderElector` in `internal/cluster/wal_leader.go` with NATS JetStream KV TTL lease acquire/renew/release semantics (SRCC-02)
- 5 unit tests pass under `-race` and `CGO_ENABLED=0`; `make verify-no-cgo` confirms no CGO leakage

## Task Commits

Each task was committed atomically:

1. **Task 1: Add Conn() accessor to NatsEventLog** - `3a57fae` (feat)
2. **TDD RED: Failing tests for WalLeaderElector** - `d721596` (test)
3. **Task 2: Implement WalLeaderElector** - `9c1c7fc` (feat)

_TDD tasks have two commits: failing test (RED) then implementation (GREEN)_

## Files Created/Modified
- `internal/eventlog/nats.go` - Added `Conn() *nats.Conn` accessor after `Ping()` method
- `internal/cluster/wal_leader.go` - WalLeaderElector with KV lease acquire/renew/release; EpochGetter; openOrCreateLeaderBucket; watchForExpiry
- `internal/cluster/wal_leader_test.go` - 5 unit tests using in-process natstest.RunServer

## Decisions Made
- Used `kv.Create` for atomic acquisition and `kv.Update` CAS for renewal — not `kv.Get` — to avoid the stale read pitfall where a node might incorrectly believe it holds a lease
- `leaseTTL=15s` is exactly `2×renewEvery=7s`, ensuring one missed renewal heartbeat does not evict the leader prematurely
- `epoch` and `isLeader` stored in `atomic.Uint64`/`atomic.Bool` — no mutex on the hot `EpochGetter` read path (called from connector goroutine)
- Connector context is never cancelled from inside `WalLeaderElector` — only `isLeader.Store(false)` is set, keeping the WAL replication connection alive for graceful shutdown
- `openOrCreateLeaderBucket` first tries `js.KeyValue` (fast path for node restarts/rolling upgrades), only calls `CreateKeyValue` on miss, with a race-safe retry of `KeyValue` if `CreateKeyValue` also fails

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- `WalLeaderElector.Run()` is ready to be wired into `root.go` via `natsEl.Conn()` (Plan 17-02)
- `EpochGetter()` method is ready for injection into `PostgresConnector.SetEpochGetter`
- All cluster package tests pass; `make verify-no-cgo` passes

---
*Phase: 17-distributed-source-coordination*
*Completed: 2026-04-30*
