---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: unknown
last_updated: "2026-03-16T12:12:25.703Z"
progress:
  total_phases: 12
  completed_phases: 10
  total_plans: 28
  completed_plans: 27
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-07)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** Phase 5: Router and Stdout Output

## Current Position

Phase: 7.4 of 10 (Backfill Pipeline Wiring)
Plan: 1 of 2 in current phase
Status: In Progress
Last activity: 2026-03-16 -- Completed 07.4-01 (SetBackfillEngine + SRC-06 re-snapshot dispatch — SRC-06 closed)

Progress: [████████░░] 40%

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
| Phase 05-router-and-stdout-output P01 | 4 | 2 tasks (TDD) | 2 files |
| Phase 05-router-and-stdout-output P02 | 2 | 2 tasks (TDD) | 4 files |
| Phase 05-router-and-stdout-output P03 | 4 | 2 tasks (TDD) | 3 files |
| Phase 07-configuration-and-multi-source P01 | 2 | 1 task (TDD) | 3 files |
| Phase 07-configuration-and-multi-source P03 | 3 | 1 task (TDD) | 2 files |
| Phase 07-configuration-and-multi-source P04 | 7 | 2 tasks | 6 files |
| Phase 07.1-infrastructure-fixes P01 | 2 | 2 tasks | 4 files |
| Phase 07.1-infrastructure-fixes P02 | 4 | 2 tasks | 2 files |
| Phase 07.2-pipeline-assembly P01 | 3 | 2 tasks | 6 files |
| Phase 07.2-pipeline-assembly P02 | 4 | 1 tasks | 2 files |
| Phase 07.3-milestone-gap-closure P01 | 1 | 1 tasks | 2 files |
| Phase 07.3-milestone-gap-closure P02 | 3 | 1 task (TDD) | 2 files |
| Phase 07.4-backfill-pipeline-wiring P01 | 2 | 2 tasks | 2 files |

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
- [Phase 05-01]: Cursor stores next-to-read seq (not last-delivered) — after delivering seq N, cursor becomes N+1; initial cursor=1 means read from seq 1; prevents infinite re-delivery
- [Phase 05-01]: NewNoopCursorStore exported as constructor — enables direct verification of LoadCursor=1 invariant in tests without Router internals
- [Phase 05-01]: dispatch serialized under mu.Lock for entire fan-out — keeps blockedGroups and cursorByPartition mutations serialized; Deliver expected to be fast (RTR-04)
- [Phase 05-01]: runPartition never returns on ReadPartition error — logs and retries with pollInterval; only ctx.Done() exits (RTR-02)
- [Phase 05-02]: RetryScheduler decoupled from Router with exported AddBlocked/BlockedCount/ForceRetryNow helpers — makes retry behavior unit-testable without a live EventLog or Router
- [Phase 05-02]: RetryRecord exported (capital R) — tests outside the package can construct and pass RetryRecord to AddBlocked; router.go retryRecord (lowercase) is unaffected
- [Phase 05-02]: StdoutWriter returns raw encoder error — RetryScheduler isPermanentError handles pipe errors; no wrapping needed in writer
- [Phase 05-03]: IsBlocked approach (option c): RetryScheduler exposes IsBlocked(consumerID, groupKey) — dispatch queries it for skip check; consumerState.blockedGroups removed entirely
- [Phase 05-03]: sync.Mutex on RetryScheduler — Tick goroutine and dispatch goroutine access rs.states concurrently; rs.mu guards all access; no deadlock (Deliver holds no locks)
- [Phase 06-02]: KaptantoMetrics uses prometheus.NewRegistry() per instance — prevents double-registration panics in tests; no global DefaultRegisterer usage
- [Phase 06-02]: HealthHandler accepts []HealthProbe at construction time — stateless after creation, safe for concurrent requests
- [Phase 06-02]: promhttp.HandlerFor(reg, HandlerOpts{Registry: reg}) — passes custom registry for both metric exposition and internal error counting
- [Phase 07-01]: Retention stored as string not time.Duration — empty string is distinguishable from explicit 0 at runtime; Event Log initializer applies 1h when empty
- [Phase 07-01]: Merge --tables replaces entire cfg.Tables map with empty TableConfig entries — per-table file config discarded when flag explicitly set
- [Phase 07-01]: No global config variable — callers create *Config and pass explicitly; package is safe for concurrent test use
- [Phase 07-03]: runPipeline is a stub for Phase 7 integration; real Phase 1-6 component wiring deferred to Phase 8+
- [Phase 07-03]: Guard checks configPath == "" and sourceDSN == "" before Merge; post-merge adds second cfg.Source == "" check to catch --source explicitly set to "" or config file with no source field
- [Phase 07-03]: signal.NotifyContext wraps cmd.Context() so test harnesses can inject contexts without real OS signals
- [Phase 07-04]: Shallow event copy (filtered := *ev) prevents mutation of shared event pointer in Router fan-out
- [Phase 07-04]: nil rowFilter / nil allowedColumns treated as pass-through — backward-compatible with all existing call sites
- [Phase 07-04]: Row filter placed before column filter in Deliver — filtered rows skip encoding work entirely
- [Phase 07.1-infrastructure-fixes]: PartitionID set by ReadPartition (not Append) — only the read path knows which partition was queried; Append derives partition internally
- [Phase 07.1-02]: CHK-02 listed under fixed_in_later_phase in 06-VERIFICATION.md — accurate attribution; Phase 6 had the defect, Phase 7.1 fixed it
- [Phase 07.2-01]: Per-table maps on SSEConsumer/GRPCConsumer: Deliver looks up rowFilters/colFilters by entry.Event.Table; nil map = pass-through
- [Phase 07.2-01]: NewSSEServer/NewGRPCServer accept rowFilters/colFilters as last two params; no external callers yet — Plan 02 wires buildTableFilters
- [Phase 07.2-02]: runPipeline uses errgroup for coordinated goroutine lifecycle; defer el.Close() after g.Wait() satisfies Badger-outlives-router invariant
- [Phase 07.2-02]: buildTableFilters returns nil maps for empty table config; nil map reads safe in Go and signal pass-through to consumers
- [Phase 07.2-02]: gRPC mode uses cfg.Port+1 for observability HTTP — gRPC H2 framing owns cfg.Port exclusively
- [Phase 07.3-milestone-gap-closure]: Drain-or-drop select replaces blocking AppendAndQueue channel send — event is durable in Badger before send, so drop from channel is safe; Router reads from eventLog.ReadPartition not connector.Events()
- [Phase 07.3-milestone-gap-closure]: ctx param kept in AppendAndQueue signature; only the select body changed — zero caller breakage
- [Phase 07.3-02]: nil prevRow to decodeColumns for OldTuple — OldTuple is already the prior row from Postgres; TOAST merge would corrupt it
- [Phase 07.3-02]: OldTuple nil guard mandatory — REPLICA IDENTITY DEFAULT updates/deletes have no OldTuple; guard keeps Before=nil and avoids nil dereference
- [Phase 07.4-backfill-pipeline-wiring]: SetBackfillEngine uses post-construction injection to break circular dependency: engine needs AppendAndQueue as appendFn, connector needs engine — SetBackfillEngine decouples them
- [Phase 07.4-backfill-pipeline-wiring]: 12b SRC-06 block is unconditional (no HasPendingBackfills guard) — slot loss implies entire table set needs re-snapshot regardless of stored backfill state
- [Phase 07.4-backfill-pipeline-wiring]: 12b placed after StartReplication so slot and publication are confirmed present before snapshot queries begin

### Pending Todos

None yet.

### Blockers/Concerns

None yet.

## Session Continuity

Last session: 2026-03-16
Stopped at: Completed 07.4-01-PLAN.md (SetBackfillEngine + SRC-06 re-snapshot dispatch)
Resume file: None
