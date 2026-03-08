---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: unknown
last_updated: "2026-03-08T13:14:39.523Z"
progress:
  total_phases: 4
  completed_phases: 4
  total_plans: 9
  completed_plans: 9
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-07)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** Phase 4: Backfill Engine

## Current Position

Phase: 4 of 10 (Backfill Engine)
Plan: 2 of 2 in current phase
Status: Phase 4 complete
Last activity: 2026-03-08 -- Completed 04-02 (BackfillEngineImpl full snapshot loop + NewWithBackfill connector wiring)

Progress: [█████░░░░░] 25%

## Performance Metrics

**Velocity:**
- Total plans completed: 3
- Average duration: 2.7 min
- Total execution time: 0.13 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01-foundation | 2 | 7 min | 3.5 min |
| 02-postgres-source-and-parser | 1 | 2 min | 2 min |

**Recent Trend:**
- Last 5 plans: 4 min, 3 min, 2 min
- Trend: establishing baseline

*Updated after each plan completion*
| Phase 02-postgres-source-and-parser P03 | 6 | 2 tasks | 7 files |
| Phase 03-event-log P01 | 15 | 2 tasks | 6 files |
| Phase 03-event-log P02 | 3 | 1 task (TDD) | 2 files |
| Phase 04-backfill-engine P01 | 4 | 2 tasks | 7 files |
| Phase 04-backfill-engine P02 | 3 | 2 tasks | 4 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: Split research's 6-phase structure into 10 phases for comprehensive depth -- separates Event Log from Backfill, Router from Output Servers, Config from HA
- [Roadmap]: MongoDB connector placed after HA (Phase 9) so it benefits from all production hardening
- [Roadmap]: Rust FFI last (Phase 10) -- performance optimization only after correctness is proven
- [01-01]: json.RawMessage without omitempty for Before/After fields -- nil serializes as JSON null, gives consumers a consistent 10-field schema regardless of operation type
- [01-01]: io.Writer injection in logging.Setup() -- enables test capture via bytes.Buffer without mocking global slog
- [01-01]: Module path github.com/kaptanto/kaptanto -- can be changed with find-and-replace before first public release
- [Phase 01-02]: NewRootCmd() factory function for test isolation — fresh cobra.Command per test, no global state contamination
- [Phase 01-02]: RunE no-op placeholder on root command — required for cobra to render flags section in help output
- [Phase 01-02]: Retention default 0s at CLI layer — actual 1h default applied at runtime when Event Log initializes
- [02-01]: modernc.org/sqlite driver name is "sqlite" (not "sqlite3") — pure Go, CGO_ENABLED=0 required by CHK-04
- [02-01]: Load returns ("", nil) for unknown sourceID — first-run safe, not an error condition
- [02-01]: WAL mode + NORMAL synchronous: db.Close() checkpoints WAL, satisfying CHK-03 graceful shutdown
- [02-01]: Open() is a package-level constructor on SQLiteStore, not an interface method — keeps CheckpointStore interface lean
- [Phase 02-03]: pgx/v5/pgconn is the correct package for replication connections — pglogrepl requires this exact type (not standalone jackc/pgconn)
- [Phase 02-03]: EvalSlotCheck exported as pure function — enables SRC-06 snapshot detection unit testing without live DB
- [Phase 02-03]: CHK-01 enforced: store.Save before SendStandbyStatusUpdate on Commit, co-located comment makes invariant visible
- [Phase 03-01]: Badger sequences pre-advanced past 0 at Open — seq=0 is unambiguous duplicate-detected sentinel
- [Phase 03-01]: seq.Next() called OUTSIDE db.Update transaction — reduces MVCC read set, gaps in sequence acceptable
- [Phase 03-01]: Fixed-width big-endian binary for all numeric key components — decimal ASCII breaks lexicographic sort order
- [Phase 03-01]: Same retention TTL on partition entry and dedup entry — dedup must not expire before the entry it guards
- [Phase 03-02]: AppendAndQueue exported as method on connector — enables black-box test of LOG-01 ordering without live Postgres; receiveLoop calls it in XLogData handler
- [Phase 03-02]: New() delegates to NewWithEventLog(nil) — nil guard preserves backward compat; Phase 4 switches to NewWithEventLog with real BadgerEventLog
- [Phase 03-02]: Append error triggers reconnect loop (not fatal crash) — Postgres re-delivers transaction from last ack'd LSN; BadgerEventLog dedup skips duplicate
- [Phase 04-backfill-engine]: PartitionOf exported from badger.go — watermark.go needs cross-package access without circular dependency
- [Phase 04-backfill-engine]: snapshotTable stubbed in 04-01; full pgx.Conn loop wired in Plan 04-02 — keeps backfill engine testable without live DB
- [Phase 04-02]: BackfillEngineImpl coexists with engine struct — separate NewBackfillEngine constructor for production use with AppendFn/OpenConnFn
- [Phase 04-02]: appendMu sync.Mutex added to PostgresConnector — serializes concurrent eventLog.Append from WAL and backfill goroutines without restructuring AppendAndQueue
- [Phase 04-02]: Backfill goroutine guarded by HasPendingBackfills() + nil check — starts only after StartReplication succeeds

### Pending Todos

None yet.

### Blockers/Concerns

None yet.

## Session Continuity

Last session: 2026-03-08
Stopped at: Completed 04-02-PLAN.md (BackfillEngineImpl wiring + NewWithBackfill connector)
Resume file: None
