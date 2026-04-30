---
gsd_state_version: 1.0
milestone: v2.0
milestone_name: Distributed Architecture
status: unknown
last_updated: "2026-04-28T18:21:52.209Z"
progress:
  total_phases: 23
  completed_phases: 23
  total_plans: 55
  completed_plans: 55
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-04-27)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v2.0 — Phase 14: Shared State Foundation

## Current Position

Phase: 16 of 17 (Partition Ownership and Active-Active Delivery)
Plan: 01 complete — 2/3 plans remaining
Status: In Progress
Last activity: 2026-04-30 — Completed 16-01 (PartitionStore atomic claim/steal/release)

Progress: [████████░░] 82% (1/3 plans complete in Phase 16)

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

### Pending Todos

None yet.

### Blockers/Concerns

- Path A (NATS JetStream) confirmed: NatsEventLog implemented and tested (Phase 15-01 complete). Path B no longer needed.
- etcd embed CGO impact must be verified before Phase 17 planning — `make verify-no-cgo` must pass with etcd embed included.

## Session Continuity

Last session: 2026-04-30T00:17:30Z
Stopped at: Completed 16-01-PLAN.md (PartitionStore atomic claim/steal/release)
Resume file: None
