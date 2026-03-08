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
- [ ] **Phase 3: Event Log** - Badger-based durable append-only store with partitioning, dedup, and TTL
- [ ] **Phase 4: Backfill Engine** - Snapshot coordination with watermark dedup, keyset cursors, crash recovery
- [ ] **Phase 5: Router and stdout Output** - Partitioned routing with per-key ordering, consumer isolation, poison pill handling, NDJSON output
- [ ] **Phase 6: SSE and gRPC Servers** - Full output server suite with consumer cursors, filtering, metrics, and health endpoint
- [ ] **Phase 7: Configuration and Multi-Source** - YAML config parsing, column filtering, SQL WHERE conditions
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
- [ ] 03-01: TBD

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
- [ ] 04-01: TBD
- [ ] 04-02: TBD

### Phase 5: Router and stdout Output
**Goal**: Events flow from the Event Log through a partitioned router to consumers, with per-key ordering preserved and a working stdout NDJSON output
**Depends on**: Phase 4
**Requirements**: RTR-01, RTR-02, RTR-03, RTR-04, RTR-05, OUT-01
**Success Criteria** (what must be TRUE):
  1. Events for the same primary key are always delivered in order, while events for different keys are delivered concurrently across partitions
  2. A slow or failing consumer does not block delivery to other consumers or other message groups within the same partition
  3. Failed events are retried with exponential backoff and moved to dead-letter after max retries
  4. Running Kaptanto with `--output stdout` produces one NDJSON line per event on standard output
**Plans**: 2 plans

Plans:
- [ ] 05-01: TBD
- [ ] 05-02: TBD

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
**Plans**: 2 plans

Plans:
- [ ] 06-01: TBD
- [ ] 06-02: TBD
- [ ] 06-03: TBD

### Phase 7: Configuration and Multi-Source
**Goal**: Kaptanto supports rich YAML configuration for multi-source setups with fine-grained per-table filtering
**Depends on**: Phase 6
**Requirements**: CFG-02, CFG-05, CFG-06
**Success Criteria** (what must be TRUE):
  1. Kaptanto parses a YAML config file defining multiple sources with per-table settings and output modes
  2. Column filtering restricts which columns appear in events for configured tables
  3. SQL WHERE condition filtering restricts which rows produce events for configured tables
**Plans**: 2 plans

Plans:
- [ ] 07-01: TBD

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
| 3. Event Log | 0/? | Not started | - |
| 4. Backfill Engine | 0/? | Not started | - |
| 5. Router and stdout Output | 0/? | Not started | - |
| 6. SSE and gRPC Servers | 0/? | Not started | - |
| 7. Configuration and Multi-Source | 0/? | Not started | - |
| 8. High Availability | 0/? | Not started | - |
| 9. MongoDB Connector | 0/? | Not started | - |
| 10. Rust FFI Acceleration | 0/? | Not started | - |
