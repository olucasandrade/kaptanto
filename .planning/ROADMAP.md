# Roadmap: Kaptanto

## Milestones

- ✅ **v1.0 Postgres CDC Binary** — Phases 1–7.7 (shipped 2026-03-16)
- 📋 **v1.1 Production Hardening** — Phases 8–10 (active)

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

### 📋 v1.1 Production Hardening (Active)

- [x] **Phase 8: High Availability** — Postgres advisory lock leader election with shared checkpoint store and automatic standby takeover (completed 2026-03-17)
- [ ] **Phase 9: MongoDB Connector** — Change Streams consumption, BSON normalization, resume token persistence, and re-snapshot on token expiry
- [ ] **Phase 10: Rust FFI Acceleration** — Optional Rust-accelerated pgoutput decoding, TOAST cache, and JSON serialization behind build tag

## Phase Details

### Phase 8: High Availability
**Goal**: Two Kaptanto instances can run against the same database; exactly one is active at any time, and the standby takes over automatically when the leader drops
**Depends on**: Phase 7
**Requirements**: HA-01, HA-02, HA-03, CHK-05
**Success Criteria** (what must be TRUE):
  1. Running two Kaptanto instances against the same database results in exactly one active WAL consumer — the other remains in standby polling; this is enforced by a Postgres session-scoped advisory lock held by the leader
  2. When the active leader process crashes or loses its database connection, the standby acquires the advisory lock within its polling interval and begins consuming WAL without operator intervention
  3. After takeover, the new leader reads the last saved checkpoint from a shared Postgres table and resumes from that LSN — no events are skipped and no duplicate processing window exceeds the checkpoint flush interval
  4. The shared Postgres checkpoint store (CHK-05) is created automatically on first run and is accessible to both instances via the same DSN
**Plans**: 3 plans
Plans:
- [ ] 08-01-PLAN.md — Postgres-backed CheckpointStore (CHK-05): PostgresStore implementing CheckpointStore against shared Postgres table
- [ ] 08-02-PLAN.md — Leader election engine (HA-01, HA-02): LeaderElector with pg_try_advisory_lock, standby polling loop, session-scoped lock semantics
- [ ] 08-03-PLAN.md — Wire HA into runPipeline (HA-03): advisory lock acquisition before pipeline start, Postgres checkpoint store swap, ha_lock health probe

### Phase 9: MongoDB Connector
**Goal**: Kaptanto captures changes from MongoDB collections via Change Streams, producing the same unified ChangeEvent format as the Postgres connector, with durable resume tokens and automatic re-snapshot on token expiry
**Depends on**: Phase 8
**Requirements**: SRC-09, SRC-10, SRC-11, SRC-12, PAR-04
**Success Criteria** (what must be TRUE):
  1. Kaptanto connects to a configured MongoDB replica set or sharded cluster, opens Change Streams on the specified collections, and emits ChangeEvents with operation insert/update/delete in the unified JSON format
  2. BSON documents from MongoDB Change Stream events are normalized into the ChangeEvent schema — _id maps to key, fullDocument maps to after, fullDocumentBeforeChange maps to before, and the resume token is stored in metadata
  3. On restart, Kaptanto reads the persisted resume token from the checkpoint store and resumes the Change Stream from that point — no full re-snapshot is needed if the token is still valid
  4. When the resume token is expired or the Change Stream returns an error indicating the token is invalid, Kaptanto automatically triggers a collection snapshot and streams WAL changes from the point the snapshot began — the same watermark coordination used by the Postgres backfill engine applies
  5. MongoDB replica set elections (primary stepdown, election, new primary) are handled transparently by the MongoDB driver — Kaptanto does not crash and resumes consuming from the new primary without operator intervention
**Plans**: TBD

### Phase 10: Rust FFI Acceleration
**Goal**: High-throughput users can opt into a Rust-accelerated build that delivers 3x throughput improvement for pgoutput decoding, TOAST cache, and JSON serialization, while the pure Go binary remains the default with no behavior change
**Depends on**: Phase 9
**Requirements**: PRF-01, PRF-03
**Success Criteria** (what must be TRUE):
  1. Building with `CGO_ENABLED=1` and the `rust` build tag produces a binary where pgoutput decoding, TOAST cache lookups, and JSON serialization are handled by Rust via FFI — the output event format is byte-for-byte identical to the pure Go build
  2. The default `go build ./cmd/kaptanto` (no build tags, CGO_ENABLED=0) produces a pure Go binary with no CGO dependency — the Rust acceleration is completely absent from this path
  3. The Makefile exposes a `make build` target for the pure Go binary and a `make build-rust` target for the Rust-accelerated binary, with clear output indicating which variant was built
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
| 8. High Availability | 3/3 | Complete   | 2026-03-17 | — |
| 9. MongoDB Connector | v1.1 | 0/? | ○ Not started | — |
| 10. Rust FFI Acceleration | v1.1 | 0/? | ○ Not started | — |
