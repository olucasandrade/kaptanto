# Roadmap: Kaptanto

## Milestones

- ✅ **v1.0 Postgres CDC Binary** — Phases 1–7.7 (shipped 2026-03-16)
- 📋 **v1.1** — Phases 8–10 (planned)

## Phases

<details>
<summary>✅ v1.0 Postgres CDC Binary (Phases 1–7.7) — SHIPPED 2026-03-16</summary>

- [x] **Phase 1: Foundation** — Shared event types, CLI skeleton, structured logging, pure Go build setup (completed 2026-03-07)
- [x] **Phase 2: Postgres Source and Parser** — WAL consumption, pgoutput decoding, TOAST cache, schema evolution, checkpoint store (completed 2026-03-08)
- [x] **Phase 3: Event Log** — Badger-based durable append-only store with partitioning, dedup, and TTL (completed 2026-03-08)
- [x] **Phase 4: Backfill Engine** — Snapshot coordination with watermark dedup, keyset cursors, crash recovery (completed 2026-03-08)
- [x] **Phase 5: Router and stdout Output** — Partitioned routing with per-key ordering, consumer isolation, poison pill handling, NDJSON output (completed 2026-03-08)
- [x] **Phase 6: SSE and gRPC Servers** — Full output server suite with consumer cursors, filtering, metrics, and health endpoint (completed 2026-03-12)
- [x] **Phase 7: Configuration and Multi-Source** — YAML config parsing, column filtering, SQL WHERE conditions (completed 2026-03-15)
- [x] **Phase 7.1: Infrastructure Fixes** [INSERTED] — LogEntry.PartitionID fix (CHK-02), Phase 6 formal verification (completed 2026-03-15)
- [x] **Phase 7.2: Pipeline Assembly** [INSERTED] — Wire all components into runPipeline; thread config filters to consumers (completed 2026-03-15)
- [x] **Phase 7.3: Milestone Gap Closure** [INSERTED] — Fix AppendAndQueue blocking channel (INT-01) and OldTuple decode for before field (INT-02) (completed 2026-03-15)
- [x] **Phase 7.4: Backfill Pipeline Wiring** [INSERTED] — Wire BackfillEngine into runPipeline, full snapshot/backfill flows live (completed 2026-03-16)
- [x] **Phase 7.5: Observability Hardening** [INSERTED] — Wire Prometheus metrics, add healthz probes, bound SSE shutdown (completed 2026-03-16)
- [x] **Phase 7.6: Backfill Correctness** [INSERTED] — Fix watermark SnapshotLSN init (BKF-02), concurrent Run race (SRC-06), SQLite pragma (BKF-03) (completed 2026-03-16)
- [x] **Phase 7.7: Stdout Metrics** [INSERTED] — Wire EventsDelivered metric into StdoutWriter (OBS-01) (completed 2026-03-16)

Full archive: `.planning/milestones/v1.0-ROADMAP.md`

</details>

### 📋 v1.1 (Planned)

- [ ] **Phase 8: High Availability** — Postgres advisory lock leader election with shared checkpoint store
- [ ] **Phase 9: MongoDB Connector** — Change Streams consumption, BSON normalization, resume token persistence
- [ ] **Phase 10: Rust FFI Acceleration** — Optional Rust-accelerated pgoutput decoding behind build tag

## Phase Details

### Phase 8: High Availability
**Goal**: Two Kaptanto instances can run against the same database with automatic failover via leader election
**Depends on**: Phase 7
**Requirements**: HA-01, HA-02, HA-03, CHK-05
**Success Criteria** (what must be TRUE):
  1. Only one Kaptanto instance actively consumes the WAL at any time, enforced by Postgres advisory locks
  2. When the active leader drops (process crash, network partition), the standby instance acquires the lock and resumes from the shared checkpoint
  3. Checkpoint state is stored in a shared Postgres table so both instances can access it
**Plans**: TBD

### Phase 9: MongoDB Connector
**Goal**: Kaptanto captures changes from MongoDB collections using Change Streams, producing the same unified event format as Postgres
**Depends on**: Phase 8
**Requirements**: SRC-09, SRC-10, SRC-11, SRC-12, PAR-04
**Success Criteria** (what must be TRUE):
  1. Kaptanto connects to MongoDB via Change Streams on configured collections and produces ChangeEvents in the unified format
  2. Resume tokens are persisted and Kaptanto resumes from the last token on restart
  3. Expired or invalid resume tokens trigger automatic re-snapshot without manual intervention
  4. MongoDB replica set elections are handled transparently without event loss
**Plans**: TBD

### Phase 10: Rust FFI Acceleration
**Goal**: High-throughput users can opt into Rust-accelerated parsing for 3x throughput improvement
**Depends on**: Phase 9
**Requirements**: PRF-01, PRF-03
**Success Criteria** (what must be TRUE):
  1. Building with `CGO_ENABLED=1` and the `rust` build tag produces a binary with Rust-accelerated pgoutput decoding, TOAST cache, and JSON serialization
  2. The Makefile supports both Go-only and Go+Rust build targets, and the pure Go build remains the default
**Plans**: TBD

## Progress

| Phase | Milestone | Plans | Status | Completed |
|-------|-----------|-------|--------|-----------|
| 1. Foundation | v1.0 | 2/2 | ✓ Complete | 2026-03-07 |
| 2. Postgres Source and Parser | v1.0 | 3/3 | ✓ Complete | 2026-03-08 |
| 3. Event Log | v1.0 | 2/2 | ✓ Complete | 2026-03-08 |
| 4. Backfill Engine | v1.0 | 2/2 | ✓ Complete | 2026-03-08 |
| 5. Router and stdout Output | v1.0 | 3/3 | ✓ Complete | 2026-03-08 |
| 6. SSE and gRPC Servers | v1.0 | 4/4 | ✓ Complete | 2026-03-12 |
| 7. Configuration and Multi-Source | v1.0 | 4/4 | ✓ Complete | 2026-03-15 |
| 7.1–7.7. Gap Closure [INSERTED] | v1.0 | 8/8 | ✓ Complete | 2026-03-16 |
| 8. High Availability | v1.1 | 0/? | ○ Not started | — |
| 9. MongoDB Connector | v1.1 | 0/? | ○ Not started | — |
| 10. Rust FFI Acceleration | v1.1 | 0/? | ○ Not started | — |
