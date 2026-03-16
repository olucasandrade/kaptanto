# Roadmap: Kaptanto

## Overview

Kaptanto delivers a single Go binary for universal database Change Data Capture. The roadmap builds the pipeline bottom-up: shared types and CLI first, then Postgres source and parser, then the durable event log, then backfill coordination, then routing and outputs, then production features (HA, multi-source config), then MongoDB as a second source, and finally optional Rust FFI acceleration. Each phase delivers a coherent, testable capability. The first five phases produce a working end-to-end Postgres CDC pipeline; phases 6-10 add production hardening, a second database, and performance optimization.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Foundation** - Shared event types, CLI skeleton, structured logging, pure Go build setup (completed 2026-03-07)
- [x] **Phase 2: Postgres Source and Parser** - WAL consumption, pgoutput decoding, TOAST cache, schema evolution, checkpoint store (completed 2026-03-08)
- [x] **Phase 3: Event Log** - Badger-based durable append-only store with partitioning, dedup, and TTL (completed 2026-03-08)
- [x] **Phase 4: Backfill Engine** - Snapshot coordination with watermark dedup, keyset cursors, crash recovery (completed 2026-03-08)
- [x] **Phase 5: Router and stdout Output** - Partitioned routing with per-key ordering, consumer isolation, poison pill handling, NDJSON output (gap closure in progress) (completed 2026-03-08)
- [x] **Phase 6: SSE and gRPC Servers** - Full output server suite with consumer cursors, filtering, metrics, and health endpoint (completed 2026-03-12)
- [x] **Phase 7: Configuration and Multi-Source** - YAML config parsing, column filtering, SQL WHERE conditions (completed 2026-03-15)
- [x] **Phase 7.1: Infrastructure Fixes** [INSERTED] - LogEntry.PartitionID fix (CHK-02), Phase 6 formal verification, REQUIREMENTS.md documentation cleanup (completed 2026-03-15)
- [x] **Phase 7.2: Pipeline Assembly** [INSERTED] - Wire all Phase 1-6 components into runPipeline; thread config column/row filters to consumers (CFG-05, CFG-06) (completed 2026-03-15)
- [x] **Phase 7.3: Milestone Gap Closure** [INSERTED] - Fix AppendAndQueue blocking channel (INT-01) and OldTuple decode for before field (INT-02) (completed 2026-03-15)
- [x] **Phase 7.4: Backfill Pipeline Wiring** [INSERTED] - Wire BackfillEngine into runPipeline so snapshot/backfill flows are live (BKF-01 through BKF-05, EVT-03, EVT-04, SRC-06) (completed 2026-03-16)
- [x] **Phase 7.5: Observability Hardening** [INSERTED] - Wire unwritten Prometheus metrics, add healthz probes, bound SSE shutdown, remove dead CLI flags (completed 2026-03-16)
- [ ] **Phase 7.6: Backfill Correctness** [INSERTED] - Fix watermark SnapshotLSN initialization (BKF-02), guard against concurrent BackfillEngine.Run (SRC-06), fix SQLiteBackfillStore pragma (BKF-03)
- [ ] **Phase 7.7: Stdout Metrics** [INSERTED] - Wire EventsDelivered metric into StdoutWriter so default output mode reports delivery metrics (OBS-01)
- [ ] **Phase 8: High Availability** - Postgres advisory lock leader election with shared checkpoint store
- [ ] **Phase 9: MongoDB Connector** - Change Streams consumption, BSON normalization, resume token persistence
- [ ] **Phase 10: Rust FFI Acceleration** - Optional Rust-accelerated pgoutput decoding behind build tag

## Phase Details

### Phase 1: Foundation
**Goal**: Establish the shared types, CLI entry point, and build infrastructure that every other phase depends on
**Depends on**: Nothing (first phase)
**Requirements**: EVT-01, EVT-02, CFG-01, OBS-03, PRF-02
**Success Criteria** (what must be TRUE):
  1. Running `kaptanto --help` displays available CLI flags and subcommands
  2. The unified ChangeEvent JSON schema is defined and serializable with all required fields (id, idempotency_key, timestamp, source, operation, table, key, before, after, metadata)
  3. Events use ULID for sortable, time-ordered IDs
  4. Structured JSON logs are emitted at configurable levels (debug, info, warn, error)
  5. `go build ./...` succeeds without CGO (pure Go default build)
**Plans**: 2 plans

Plans:
- [x] 01-01-PLAN.md — Go module init, ChangeEvent types, ULID generation, structured logging
- [x] 01-02-PLAN.md — CLI skeleton with cobra flags, Makefile, pure-Go build verification

### Phase 2: Postgres Source and Parser
**Goal**: Kaptanto connects to a Postgres database, consumes the WAL stream, decodes pgoutput messages into ChangeEvents, and persists checkpoints crash-safely
**Depends on**: Phase 1
**Requirements**: SRC-01, SRC-02, SRC-03, SRC-04, SRC-05, SRC-06, SRC-07, SRC-08, PAR-01, PAR-02, PAR-03, PAR-05, CHK-01, CHK-03, CHK-04
**Success Criteria** (what must be TRUE):
  1. Kaptanto connects to Postgres via logical replication slot, auto-creates slot and publication, and begins receiving WAL events
  2. INSERT, UPDATE, and DELETE operations on configured tables produce correctly decoded ChangeEvents with complete row data (including TOAST columns)
  3. Schema changes (adding/removing columns) are detected and subsequent events reflect the new schema
  4. Kaptanto reconnects automatically after connection loss with backoff, supports multi-host DSN for failover, and detects missing replication slots
  5. On restart, Kaptanto resumes from the last checkpointed LSN without re-processing already-acknowledged events
**Plans**: 3 plans

Plans:
- [x] 02-01-PLAN.md — SQLite checkpoint store (CheckpointStore interface, WAL mode, Save/Load LSN)
- [ ] 02-02-PLAN.md — pgoutput parser (RelationCache, TOASTCache, decodeColumns, idempotency key)
- [ ] 02-03-PLAN.md — PostgresConnector (replication loop, keepalive, backoff, slot/publication, validation)

### Phase 3: Event Log
**Goal**: Every parsed event is durably stored in an embedded Badger database before the source checkpoint is advanced
**Depends on**: Phase 2
**Requirements**: LOG-01, LOG-02, LOG-03, LOG-04
**Success Criteria** (what must be TRUE):
  1. Every parsed ChangeEvent is written to Badger before the source LSN is acknowledged to Postgres
  2. Events are distributed across partitions by hash of grouping key, and duplicate event IDs are silently deduplicated on write
  3. Events automatically expire and are cleaned up after the configured retention period
**Plans**: 2 plans

Plans:
- [ ] 03-01-PLAN.md — BadgerEventLog implementation (EventLog interface, key encoding, Append with dedup+TTL, ReadPartition, TDD)
- [ ] 03-02-PLAN.md — Connector wiring (add EventLog field to PostgresConnector, Append before store.Save in receiveLoop)

### Phase 4: Backfill Engine
**Goal**: Kaptanto can snapshot existing table data and coordinate it with the live WAL stream so consumers see a complete, consistent view
**Depends on**: Phase 3
**Requirements**: BKF-01, BKF-02, BKF-03, BKF-04, BKF-05, EVT-03, EVT-04
**Success Criteria** (what must be TRUE):
  1. Running Kaptanto against a table with existing rows produces snapshot "read" events for all rows using keyset cursor pagination
  2. Concurrent writes during a snapshot are correctly deduplicated via watermark coordination -- no stale reads appear in the event stream
  3. A crash during backfill resumes from the last persisted cursor position without re-emitting already-captured rows
  4. Control events (snapshot_complete, table_added, schema_change) signal pipeline state transitions
  5. All snapshot strategies work: snapshot_and_stream, stream_only, snapshot_only, snapshot_deferred, snapshot_partial
**Plans**: 2 plans

Plans:
- [x] 04-01-PLAN.md — Backfill Engine core package (BackfillState, KeysetCursor, WatermarkChecker, BatchOptimizer, BackfillStore, snapshot read + control events, TDD)
- [x] 04-02-PLAN.md — Connector wiring (NewWithBackfill, backfill goroutine launch, AppendAndQueue serialization)

### Phase 5: Router and stdout Output
**Goal**: Events flow from the Event Log through a partitioned router to consumers, with per-key ordering preserved and a working stdout NDJSON output
**Depends on**: Phase 4
**Requirements**: RTR-01, RTR-02, RTR-03, RTR-04, RTR-05, OUT-01
**Success Criteria** (what must be TRUE):
  1. Events for the same primary key are always delivered in order, while events for different keys are delivered concurrently across partitions
  2. A slow or failing consumer does not block delivery to other consumers or other message groups within the same partition
  3. Failed events are retried with exponential backoff and moved to dead-letter after max retries
  4. Running Kaptanto with `--output stdout` produces one NDJSON line per event on standard output
**Plans**: 3 plans

Plans:
- [x] 05-01-PLAN.md — Router core: Consumer interface, ConsumerCursorStore interface, partition goroutines, message group blocking (RTR-01, RTR-02, RTR-03, RTR-04)
- [x] 05-02-PLAN.md — Retry scheduler and stdout writer: exponential backoff, dead-letter, StdoutWriter NDJSON output (RTR-05, OUT-01)
- [x] 05-03-PLAN.md — Gap closure: wire RetryScheduler into Router.Run and Router.dispatch, remove duplicate retryRecord type (RTR-05)

### Phase 6: SSE and gRPC Servers
**Goal**: Multiple independent consumers can connect via SSE or gRPC to receive events, with proper resume, backpressure, filtering, and observability
**Depends on**: Phase 5
**Requirements**: OUT-02, OUT-03, OUT-04, OUT-05, OUT-06, OUT-07, OUT-08, CHK-02, CFG-03, CFG-04, OBS-01, OBS-02
**Success Criteria** (what must be TRUE):
  1. Multiple SSE clients can independently connect and receive events, resume via Last-Event-ID after disconnect, and stay alive through proxies via periodic ping comments
  2. gRPC clients can subscribe to server-streaming events, acknowledge delivery, and benefit from HTTP/2 native backpressure
  3. Consumer cursors are flushed to the checkpoint store periodically and consumers resume from their last position on reconnect
  4. Table and operation filtering restricts which events each consumer receives
  5. Prometheus metrics (lag, throughput, errors, consumer lag) and /healthz endpoint are operational
**Plans**: 4 plans

Plans:
- [ ] 06-01-PLAN.md — SQLiteCursorStore (CHK-02) + EventFilter table/operation allow-list (CFG-03, CFG-04)
- [ ] 06-02-PLAN.md — Prometheus metrics with custom registry (OBS-01) + /healthz endpoint (OBS-02)
- [ ] 06-03-PLAN.md — SSE server: SSEConsumer + SSEServer with CORS, ping, Last-Event-ID resume (OUT-02, OUT-03, OUT-04, OUT-05)
- [ ] 06-04-PLAN.md — gRPC server: proto + GRPCConsumer channel bridge + Subscribe/Acknowledge RPCs (OUT-06, OUT-07, OUT-08)

### Phase 7: Configuration and Multi-Source
**Goal**: Kaptanto supports YAML configuration with fine-grained per-table column and row filtering, and the root command runs the full pipeline
**Depends on**: Phase 6
**Requirements**: CFG-02, CFG-05, CFG-06
**Success Criteria** (what must be TRUE):
  1. Kaptanto parses a YAML config file defining multiple sources with per-table settings and output modes
  2. Column filtering restricts which columns appear in events for configured tables
  3. SQL WHERE condition filtering restricts which rows produce events for configured tables
**Plans**: 3 plans

Plans:
- [ ] 07-01-PLAN.md — Config package: Config/TableConfig structs, Load(), Defaults(), Merge() with CLI flag precedence (CFG-02)
- [ ] 07-02-PLAN.md — Column filter (ApplyColumnFilter) and WHERE row filter (RowFilter with mini expression evaluator) (CFG-05, CFG-06)
- [ ] 07-03-PLAN.md — Root command wiring: replace RunE no-op with real pipeline startup using config + filters (CFG-02, CFG-05, CFG-06)

### Phase 7.1: Infrastructure Fixes [INSERTED]
**Goal**: Fix the CHK-02 cursor-correctness defect, produce a formal Phase 6 VERIFICATION.md, and synchronize REQUIREMENTS.md checkboxes with actual implementation status
**Depends on**: Phase 7
**Requirements**: CHK-02
**Gap Closure**: Closes gaps from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `eventlog.LogEntry` has a `PartitionID uint32` field populated by `BadgerEventLog.ReadPartition`
  2. `SSEConsumer.Deliver` uses `entry.PartitionID` (not hardcoded 0) when calling `SaveCursor` — consumers resume from the correct per-partition position on reconnect
  3. A `06-VERIFICATION.md` exists in `.planning/phases/06-sse-and-grpc-servers/` with `status: passed`
  4. REQUIREMENTS.md checkboxes for PAR-01..PAR-05, OUT-02..OUT-08, CHK-02, CFG-03, CFG-04, OBS-01, OBS-02 are updated to reflect actual implementation status
**Plans**: 2 plans

Plans:
- [ ] 07.1-01-PLAN.md — CHK-02 code fix: LogEntry.PartitionID + SSEConsumer.Deliver wiring (TDD)
- [ ] 07.1-02-PLAN.md — Phase 6 VERIFICATION.md + REQUIREMENTS.md checkbox sync

### Phase 7.2: Pipeline Assembly [INSERTED]
**Goal**: The `kaptanto` binary is a working CDC pipeline — running `kaptanto --source postgres://...` connects to Postgres, streams WAL changes, and delivers events via stdout, SSE, or gRPC, with config-driven column and row filtering applied per table
**Depends on**: Phase 7.1
**Requirements**: CFG-05, CFG-06, OUT-01, OUT-02, OUT-03, OUT-04, OUT-05, OUT-06, OUT-07, OUT-08, OBS-01, OBS-02
**Gap Closure**: Closes gaps from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `runPipeline` opens a `BadgerEventLog`, creates a `PostgresConnector` (with backfill), starts a `Router`, and wires at least one output (stdout when `--output stdout`)
  2. Running `kaptanto --source postgres://... --output stdout` produces NDJSON CDC events on stdout for INSERT/UPDATE/DELETE operations
  3. `config.TableConfig.Columns` is passed as `allowedColumns` to SSE and gRPC consumer constructors — column filtering is config-driven, not hardcoded nil
  4. `config.TableConfig.Where` is parsed via `output.ParseRowFilter` and passed as `rowFilter` to SSE and gRPC consumer constructors — row filtering is config-driven, not hardcoded nil
  5. Prometheus metrics and `/healthz` endpoints are registered and reachable when `--output sse` or `--output grpc` is used
**Plans**: 2 plans

Plans:
- [ ] 07.2-01-PLAN.md — Update SSEServer/GRPCServer with per-table filter maps; update consumer constructors (CFG-05, CFG-06, OUT-02..OUT-08)
- [ ] 07.2-02-PLAN.md — Implement runPipeline: wire BadgerEventLog, PostgresConnector, Router, output servers, observability (OUT-01..OUT-08, OBS-01, OBS-02, CFG-05, CFG-06)

### Phase 7.3: Milestone Gap Closure [INSERTED]
**Goal**: Close the two integration gaps identified by the v1.0 milestone audit: (1) `AppendAndQueue` blocks the WAL receive loop when `connector.Events()` is never drained, stalling all E2E flows after 1024 events; (2) `handleUpdate` and `handleDelete` discard Postgres `OldTuple` data, leaving `before` null for every UPDATE/DELETE event even with REPLICA IDENTITY FULL
**Depends on**: Phase 7.2
**Requirements**: SRC-01, SRC-03, CHK-01, LOG-01, EVT-01, PAR-01
**Gap Closure**: Closes INT-01 and INT-02 from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `AppendAndQueue` never blocks the WAL receive loop regardless of how many events accumulate — the channel send is non-blocking (drain-or-drop) since the Router reads from `eventLog.ReadPartition`, not from `connector.Events()`
  2. `go test ./internal/source/postgres/...` passes with a new test verifying `AppendAndQueue` does not block when the channel is full
  3. For an UPDATE event where `m.OldTuple != nil`, the resulting `ChangeEvent.Before` is non-nil and contains the prior row data
  4. For a DELETE event where `m.OldTuple != nil`, the resulting `ChangeEvent.Before` is non-nil and contains the deleted row data
  5. `go test ./internal/parser/pgoutput/...` passes with new tests asserting `Before` is populated when `OldTuple` is present in the test fixture
**Plans**: 0 plans

### Phase 7.4: Backfill Pipeline Wiring [INSERTED]
**Goal**: Wire `BackfillEngineImpl` into `runPipeline` so the full snapshot/backfill flow is live — snapshot reads, watermark dedup, cursor persistence, adaptive batch sizing, all five snapshot strategies, and slot-loss re-snapshot trigger all function at runtime
**Depends on**: Phase 7.3
**Requirements**: BKF-01, BKF-02, BKF-03, BKF-04, BKF-05, EVT-03, EVT-04, SRC-06
**Gap Closure**: Closes gaps from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `NewBackfillEngine` is called in `runPipeline` and the result is passed as the fifth argument to `postgres.NewWithBackfill` (replacing the current `nil`)
  2. The connector's backfill goroutine starts at runtime — snapshot rows flow through EventLog → Router → output consumers
  3. Snapshot "read" events (`operation: "read"`) appear in the event stream when the table has existing rows
  4. A slot-loss scenario (SRC-06) correctly enqueues a re-snapshot via the wired BackfillEngine
  5. `go test ./...` passes with new integration-level tests confirming snapshot events reach the Router
**Plans**: 2 plans

Plans:
- [ ] 07.4-01-PLAN.md — Add SetBackfillEngine setter to PostgresConnector; wire SRC-06 re-snapshot dispatch in connectAndStream
- [ ] 07.4-02-PLAN.md — Wire BackfillEngineImpl into runPipeline with buildBackfillConfigs, SQLiteBackfillStore, and WatermarkChecker

### Phase 7.5: Observability Hardening [INSERTED]
**Goal**: All registered Prometheus metrics are written from production code paths, `/healthz` probes real component health, SSE shutdown is bounded, and dead CLI flags are cleaned up
**Depends on**: Phase 7.4
**Requirements**: OBS-01, OBS-02, CFG-01
**Gap Closure**: Closes tech debt from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `SourceLagBytes`, `ConsumerLag`, `CheckpointFlushes`, and `ErrorsTotal` Prometheus metrics are incremented/set from the production code paths that generate those events
  2. `/healthz` returns 503 with diagnostic JSON when Badger, SQLite, or Postgres connectivity is degraded — health probes are wired via `NewHealthHandler`
  3. `SSEServer` shutdown uses a timeout-bounded context (e.g., 5s) so graceful shutdown completes even under active client connections
  4. `--ha` and `--node-id` CLI flags are either removed or backed by `config.Config` fields with documented behavior
**Plans**: 2 plans

Plans:
- [ ] 07.5-01-PLAN.md — Wire SourceLagBytes, ConsumerLag, CheckpointFlushes, ErrorsTotal metrics + add Ping methods to storage components
- [ ] 07.5-02-PLAN.md — Wire real health probes to /healthz, bound SSE/gRPC shutdown to 5s, add HA/NodeID to Config

### Phase 7.6: Backfill Correctness [INSERTED]
**Goal**: Fix three audit-identified bugs in the backfill subsystem: watermark deduplication uses `snapshotLSN=0` (BKF-02), concurrent `Run` launches create a data race (SRC-06), and `OpenSQLiteBackfillStore` uses an unreliable URI pragma format (BKF-03)
**Depends on**: Phase 7.5
**Requirements**: BKF-02, SRC-06, BKF-03
**Gap Closure**: Closes gaps from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `snapshotTable` assigns `state.SnapshotLSN` to the current WAL flush LSN before entering the snapshot row loop — watermark dedup correctly suppresses only rows superseded by *newer* WAL events
  2. `BackfillEngineImpl.Run` is protected against concurrent execution (or `connectAndStream` prevents double-launch) — `go test -race ./...` passes with no data race on backfill state
  3. `OpenSQLiteBackfillStore` applies WAL and synchronous pragmas via `db.Exec` after open, consistent with `SQLiteStore` and `SQLiteCursorStore` — no URI pragma parameters in the DSN
**Plans**: 1 plan

Plans:
- [ ] 07.6-01-PLAN.md — Fix BKF-02 (SnapshotLSN), SRC-06 (concurrent Run), BKF-03 (SQLite pragma)

### Phase 7.7: Stdout Metrics [INSERTED]
**Goal**: The default output mode (stdout) emits `EventsDelivered` Prometheus metrics so observability works for all deployments, not just SSE/gRPC
**Depends on**: Phase 7.6
**Requirements**: OBS-01
**Gap Closure**: Closes OBS-01 gap from v1.0 milestone audit
**Success Criteria** (what must be TRUE):
  1. `StdoutWriter` has a `SetMetrics` method (or constructor parameter) wired to `KaptantoMetrics`
  2. `StdoutWriter.Deliver` increments `kaptanto_events_delivered_total` with correct label values on each successful delivery
  3. `runPipeline` passes the shared metrics instance to `StdoutWriter` — the counter is non-zero after events flow through stdout mode
**Plans**: 0 plans

### Phase 8: High Availability
**Goal**: Two Kaptanto instances can run against the same database with automatic failover via leader election
**Depends on**: Phase 7
**Requirements**: HA-01, HA-02, HA-03, CHK-05
**Success Criteria** (what must be TRUE):
  1. Only one Kaptanto instance actively consumes the WAL at any time, enforced by Postgres advisory locks
  2. When the active leader drops (process crash, network partition), the standby instance acquires the lock and resumes from the shared checkpoint
  3. Checkpoint state is stored in a shared Postgres table so both instances can access it
**Plans**: 2 plans

Plans:
- [ ] 08-01: TBD

### Phase 9: MongoDB Connector
**Goal**: Kaptanto captures changes from MongoDB collections using Change Streams, producing the same unified event format as Postgres
**Depends on**: Phase 8
**Requirements**: SRC-09, SRC-10, SRC-11, SRC-12, PAR-04
**Success Criteria** (what must be TRUE):
  1. Kaptanto connects to MongoDB via Change Streams on configured collections and produces ChangeEvents in the unified format
  2. Resume tokens are persisted and Kaptanto resumes from the last token on restart
  3. Expired or invalid resume tokens trigger automatic re-snapshot without manual intervention
  4. MongoDB replica set elections are handled transparently without event loss
**Plans**: 2 plans

Plans:
- [ ] 09-01: TBD

### Phase 10: Rust FFI Acceleration
**Goal**: High-throughput users can opt into Rust-accelerated parsing for 3x throughput improvement
**Depends on**: Phase 9
**Requirements**: PRF-01, PRF-03
**Success Criteria** (what must be TRUE):
  1. Building with `CGO_ENABLED=1` and the `rust` build tag produces a binary with Rust-accelerated pgoutput decoding, TOAST cache, and JSON serialization
  2. The Makefile supports both Go-only and Go+Rust build targets, and the pure Go build remains the default
**Plans**: 2 plans

Plans:
- [ ] 10-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8 -> 9 -> 10

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation | 2/2 | Complete   | 2026-03-07 |
| 2. Postgres Source and Parser | 3/3 | Complete   | 2026-03-08 |
| 3. Event Log | 2/2 | Complete   | 2026-03-08 |
| 4. Backfill Engine | 2/2 | Complete   | 2026-03-08 |
| 5. Router and stdout Output | 3/3 | Complete   | 2026-03-08 |
| 6. SSE and gRPC Servers | 4/4 | Complete   | 2026-03-12 |
| 7. Configuration and Multi-Source | 4/4 | Complete   | 2026-03-15 |
| 7.1. Infrastructure Fixes [INSERTED] | 2/2 | Complete   | 2026-03-15 |
| 7.2. Pipeline Assembly [INSERTED] | 2/2 | Complete | 2026-03-15 |
| 7.3. Milestone Gap Closure [INSERTED] | 2/2 | Complete | 2026-03-15 |
| 7.4. Backfill Pipeline Wiring [INSERTED] | 2/2 | Complete    | 2026-03-16 |
| 7.5. Observability Hardening [INSERTED] | 2/2 | Complete | 2026-03-16 |
| 7.6. Backfill Correctness [INSERTED] | 0/1 | In progress | - |
| 7.7. Stdout Metrics [INSERTED] | 0/0 | Not started | - |
| 8. High Availability | 0/? | Not started | - |
| 9. MongoDB Connector | 0/? | Not started | - |
| 10. Rust FFI Acceleration | 0/? | Not started | - |
