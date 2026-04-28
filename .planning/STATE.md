---
gsd_state_version: 1.0
milestone: v2.0
milestone_name: Distributed Architecture
status: unknown
last_updated: "2026-04-28T18:03:21.631Z"
progress:
  total_phases: 23
  completed_phases: 22
  total_plans: 55
  completed_plans: 54
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-04-27)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v2.0 — Phase 14: Shared State Foundation

## Current Position

Phase: 15 of 17 (Distributed Event Log)
Plan: 01 complete — Phase 15 In Progress
Status: In Progress
Last activity: 2026-04-28 — Completed 15-01 (NatsEventLog implementation)

Progress: [████░░░░░░] 40% (1/2 plans complete in Phase 15)

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

### Pending Todos

None yet.

### Blockers/Concerns

- Path A (NATS JetStream) confirmed: NatsEventLog implemented and tested (Phase 15-01 complete). Path B no longer needed.
- etcd embed CGO impact must be verified before Phase 17 planning — `make verify-no-cgo` must pass with etcd embed included.

## Session Continuity

Last session: 2026-04-28T00:21:51Z
Stopped at: Completed 15-01-PLAN.md (NatsEventLog implementation — Phase 15 Plan 01 complete)
Resume file: None
