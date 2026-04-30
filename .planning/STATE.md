---
gsd_state_version: 1.0
milestone: v2.0
milestone_name: Distributed Architecture
status: unknown
last_updated: "2026-04-30T00:35:06.406Z"
progress:
  total_phases: 24
  completed_phases: 24
  total_plans: 58
  completed_plans: 58
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-04-27)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v2.0 — Phase 14: Shared State Foundation

## Current Position

Phase: 17 of 17 (Distributed Source Coordination)
Plan: 02 complete — Phase 17 in progress (2/3 plans done)
Status: Phase 17 In Progress
Last activity: 2026-04-30 — Completed 17-02 (epoch-fenced sendStandbyStatus: epochGetter field + SetEpochGetter + ShouldSendStandby guard, SRCC-01)

Progress: [███████░░░] 67% (2/3 plans complete in Phase 17)

## Performance Metrics

**Velocity:**
- Total plans completed: 0 (v2.0)
- Average duration: —
- Total execution time: —

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| - | - | - | - |

*Updated after each plan completion*
| Phase 14 P02 | 196 | 2 tasks | 4 files |
| Phase 14-shared-state-foundation P01 | 4 | 2 tasks | 3 files |
| Phase 14-shared-state-foundation P03 | 5 | 2 tasks | 3 files |
| Phase 15 P01 | 8 | 2 tasks | 5 files |
| Phase 15 P02 | 3 | 2 tasks | 2 files |
| Phase 16 P01 | 3 | 2 tasks | 2 files |
| Phase 16 P02 | 4 | 2 tasks | 5 files |
| Phase 16 P03 | 4 | 2 tasks | 2 files |
| Phase 17 P02 | 4 | 1 task (TDD) | 2 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- Path A (NATS JetStream + etcd) is the recommended distributed stack — NATS sidecar (15MB Go binary) for event log, etcd embedded peer for coordination. Path B (hashicorp/raft + Badger) is viable if a time-boxed spike confirms Badger v4 raft-badger compatibility.
- Phase 14 is the critical-path unlock — shared cursor state must exist before partition handoff (Phase 16) is possible.
- 64-partition FNV-1a scheme is fixed for lifetime of cluster — changing it invalidates all cursor positions.
- WAL source does not scale horizontally (hard Postgres protocol constraint) — distribute delivery side only.
- [Phase 14]: markOffline uses context.Background() so DELETE executes after graceful shutdown ctx cancellation
- [Phase 14]: StaleNodes returns non-nil empty slice to avoid nil-check bugs in callers
- [Phase 14-shared-state-foundation]: PostgresCursorStore uses uint32 for partitionID to match ConsumerCursorStore interface exactly
- [Phase 14-shared-state-foundation]: Test suite uses nil pgx.Conn so all PostgresCursorStore tests run without Postgres (dirty-map paths independently testable)
- [Phase 14-shared-state-foundation]: Snapshot restore in flush() only inserts if key not already dirty, preventing overwrite of newer SaveCursor calls during in-flight transaction
- [Phase 14-shared-state-foundation]: cursorRun/cursorPing/cursorSetMetrics closures abstract concrete method dispatch through interface variable — avoids type assertions at each call site
- [Phase 14-shared-state-foundation]: runMongoPipeline signature updated to router.ConsumerCursorStore interface + cursorRun func — MongoDB path cluster-mode compatible
- [Phase 14-shared-state-foundation]: Ping(ctx) added to PostgresCursorStore for /healthz probe (not on original store, added as Rule 2 auto-fix)
- [Phase 15]: NatsEventLog.Append returns seq=0 for duplicates (PubAck.Duplicate=true) matching BadgerEventLog LOG-03 sentinel
- [Phase 15]: Stream Replicas=max(1, len(peers)+1) avoids single-node stream creation failure while supporting 3-node cluster R=3
- [Phase 15]: StreamConfig.Duplicates=retention (not default 2m) to prevent WAL re-delivery after crash creating duplicates
- [Phase 15]: AppendBatch is sequential loop over Append (no native NATS batch tx) — CHK-01 safe, each Append blocks until PubAck
- [Phase 15-02]: elPing func variable captures Ping from concrete type before upcasting to EventLog interface — avoids type assertions in health probe
- [Phase 15-02]: /healthz probe renamed from 'badger' to 'eventlog' — neutral label works for both BadgerEventLog and NatsEventLog
- [Phase 15-02]: NatsClusterPort=0 in Defaults() preserves "not set" distinction; 0 → 6222 applied at pipeline start in runPipeline
- [Phase 16-01]: pgx.ErrNoRows from ClaimUnclaimed UPDATE RETURNING is a normal race loss — silently skipped, not surfaced as error
- [Phase 16-01]: Non-nil empty slice invariant applied to ClaimUnclaimed, StealStalePartitions, and ListOwned — matches StaleNodes contract
- [Phase 16-01]: EpochFor reads in-memory epochs map under RLock — avoids DB round-trip for hot-path partition validation in Plan 03
- [Phase 16-01]: OpenPartitionStore seeds 64 rows via INSERT ON CONFLICT DO NOTHING — idempotent across concurrent multi-node starts
- [Phase 16-02]: PartitionManager constructed before Router (nil rtr) — circular dep broken by SetRouter injection after NewRouter
- [Phase 16-02]: epochCursorStore.SaveCursor drops unowned partitions silently (nil error) — zombie nodes cannot advance cursors (DLVR-02)
- [Phase 16-02]: Router.Run reads ownedPartitions under RLock snapshot at entry — avoids locking inside goroutine launch loop
- [Phase 16-02]: allPartitions(n) helper produces [0..n-1] slice so nil ownedPartitions is byte-for-byte identical to pre-Phase-16 behavior
- [Phase 16-02]: PartitionManager.ReleaseAll NOT called inside Run — root.go calls it after g.Wait() so cursor flush completes first
- [Phase 16]: Cluster setup moved entirely before NewRouter — DLVR-02 requires epochCursorStore to be ready before Router is constructed
- [Phase 16]: pm.ReleaseAll called in root.go after g.Wait() — canonical shutdown path; pm.Run does NOT call ReleaseAll internally
- [Phase 16]: fakeEventLogForCmd in cmd_test package satisfies eventlog.EventLog interface for compile-guard test without cross-package test helper imports
- [Phase 17-02]: ShouldSendStandby exported (not unexported) so postgres_test package can test epoch guard logic without reflection or build tags
- [Phase 17-02]: epochGetter func pointer set once before Run starts, never mutated during Run — no mutex needed in connector (WalLeaderElector reads its own atomic.Bool internally)
- [Phase 17-02]: Zombie node drops standby update (returns nil) rather than cancelling ctx — ctx cancellation closes replication slot which can corrupt in-flight events; wal_receiver_timeout is the correct fence
- [Phase 17-02]: nil epochGetter path is byte-for-byte identical to pre-Phase-17 — ShouldSendStandby(nil) returns true unconditionally

### Pending Todos

None yet.

### Blockers/Concerns

- Path A (NATS JetStream) confirmed: NatsEventLog implemented and tested (Phase 15-01 complete). Path B no longer needed.
- etcd embed CGO impact must be verified before Phase 17 planning — `make verify-no-cgo` must pass with etcd embed included.

## Session Continuity

Last session: 2026-04-30T16:57:51Z
Stopped at: Completed 17-02-PLAN.md (epoch-fenced sendStandbyStatus: SetEpochGetter + ShouldSendStandby, SRCC-01)
Resume file: None
