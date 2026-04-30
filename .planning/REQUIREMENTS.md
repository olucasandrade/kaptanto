# Requirements: Kaptanto

**Defined:** 2026-04-27
**Core Value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## v2.0 Requirements

Requirements for v2.0 Distributed Architecture milestone. Each maps to roadmap phases (Phases 14–17).

### Shared State (STATE)

- [x] **STATE-01**: User can deploy multiple Kaptanto nodes that share consumer delivery positions — when a node fails, a surviving node resumes delivery from the last persisted cursor without gaps or re-delivery of already-acknowledged events
- [x] **STATE-02**: Cluster tracks active nodes in a shared Postgres table (identity, address, last heartbeat, partition assignments) — stale nodes are detected and their partitions reassigned within one heartbeat interval
- [x] **STATE-03**: Backfill progress is persisted to the shared store so that if the backfill-running node crashes, any surviving node can resume the snapshot from the last committed keyset cursor without restarting from scratch

### Event Log (EVLOG)

- [x] **EVLOG-01**: Any single node failure does not lose events that were successfully appended — the event log is Raft-replicated across NATS JetStream nodes with quorum-based durability
- [x] **EVLOG-02**: CHK-01 holds cluster-wide — source LSN/resume token does not advance until a quorum of NATS JetStream nodes confirms the append is durable
- [x] **EVLOG-03**: The Kaptanto binary remains pure Go (CGO_ENABLED=0 preserved) — NATS JetStream runs as a co-located sidecar process; the cluster can be started with a single `kaptanto start --cluster` command that also starts the NATS sidecar

### Delivery (DLVR)

- [x] **DLVR-01**: A node joining a running cluster automatically claims unclaimed partitions from the coordinator and begins serving SSE and gRPC consumers for those partitions without operator intervention
- [x] **DLVR-02**: When a node leaves (gracefully or via crash), its partitions are reassigned to surviving nodes using a two-phase handoff — the old node drains all in-flight events before the new node begins consuming, and epoch fencing tokens prevent any zombie write from a partitioned-then-reconnected node
- [x] **DLVR-03**: Multiple Kaptanto nodes can simultaneously serve SSE and gRPC consumers, each node serving only its owned partitions — consumers can connect to any node and receive events for that node's partitions
- [x] **DLVR-04**: Per-key ordering (RTR-04) is preserved across partition reassignments — events for any given primary key always arrive at downstream consumers in LSN order, including during node join and leave events

### Source Coordination (SRCC)

- [x] **SRCC-01**: Epoch fencing tokens prevent a zombie WAL leader from advancing the Postgres replication slot LSN — a node that was partitioned and reconnects after a new leader was elected cannot write events or advance the confirmed_flush_lsn
- [ ] **SRCC-02**: Kaptanto cluster leader election is backed by etcd consensus (embedded peer in each binary) — leader election does not require a separate coordination service and survives any single node failure
- [ ] **SRCC-03**: MongoDB Change Stream resume tokens are written synchronously to the shared store before any event at that position is acknowledged — a node crash does not lose resume token progress, and the replacement node resumes from the correct shared-store position without re-processing already-logged events

## v2.x Requirements

Deferred to future releases. Acknowledged but not in current roadmap.

### Delivery

- **DLVR-05**: Consumer auto-routing — any node accepts any consumer connection and transparently proxies to the partition-owning node; consumer does not need to know which node owns which partition
- **DLVR-06**: `/cluster` HTTP endpoint — shows node membership, partition ownership map, per-partition lag; extends existing `/healthz` and `/metrics`

### Backfill

- **BKF-D-01**: Partition-aware distributed backfill — assign non-overlapping keyset ranges to different nodes for parallel initial snapshots; requires stable membership and linearizable watermark store
- **BKF-D-02**: Distributed backfill watermark coordination — WatermarkChecker reads shared store with linearizable consistency for multi-node backfill+WAL coordination; addresses BKF-02 in distributed setting

### Source

- **SRCC-04**: MongoDB shard-level parallelism — one Change Stream consumer per MongoDB shard, each owned by a different Kaptanto node

### Architecture

- **ARCH-01**: Embedded Raft coordination mode (Path B) — zero external processes; hashicorp/raft + Badger replaces NATS JetStream; for deployments where no external sidecar is acceptable; requires Badger v4 raft-badger compatibility validation

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Dynamic partition count change | Changing FNV-1a partition count invalidates all existing cursor positions; 64 partitions is fixed for the lifetime of a cluster |
| Exactly-once delivery guarantee | True exactly-once requires distributed transactions; at-least-once + ULID idempotency key is the correct production pattern (Debezium, Kafka all deliver at-least-once) |
| Multiple WAL readers per Postgres slot | Hard Postgres protocol constraint — one active walsender per slot; attempting this corrupts the WAL stream; scale delivery side, not source side |
| Cross-datacenter active/active replication | Requires synchronous cross-DC Raft (50-200ms latency per append on WAN) plus conflict resolution; separate research milestone, not v2.0 |
| Kafka as internal coordination mechanism | JVM dependency, violates zero-infrastructure-deps philosophy; Kafka can be added as a _sink adapter_ later, not as internal coordination |
| MySQL connector | Orthogonal to distributed architecture work; separate source milestone |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| STATE-01 | Phase 14 | Complete |
| STATE-02 | Phase 14 | Complete |
| STATE-03 | Phase 14 | Complete |
| EVLOG-01 | Phase 15 | Complete |
| EVLOG-02 | Phase 15 | Complete |
| EVLOG-03 | Phase 15 | Complete |
| DLVR-01 | Phase 16 | Complete |
| DLVR-02 | Phase 16 | Complete |
| DLVR-03 | Phase 16 | Complete |
| DLVR-04 | Phase 16 | Complete |
| SRCC-01 | Phase 17 | Complete |
| SRCC-02 | Phase 17 | Pending |
| SRCC-03 | Phase 17 | Pending |

**Coverage:**
- v2.0 requirements: 13 total
- Mapped to phases: 13
- Unmapped: 0 ✓

---
*Requirements defined: 2026-04-27*
*Last updated: 2026-04-27 after initial v2.0 definition*
