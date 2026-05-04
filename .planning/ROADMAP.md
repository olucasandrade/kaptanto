# Roadmap: Kaptanto

## Milestones

- ✅ **v1.0 Postgres CDC Binary** — Phases 1–7.7 (shipped 2026-03-16)
- ✅ **v1.1 Production Hardening** — Phases 8–10 (shipped 2026-03-20)
- ✅ **v1.2 Benchmark Suite** — Phases 11–13 (shipped 2026-03-21)
- ✅ **v2.0 Distributed Architecture** — Phases 14–18 (shipped 2026-05-03)
- 🚧 **v2.1 Queue Sinks** — Phases 19–23 (in progress)

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

<details>
<summary>✅ v1.1 Production Hardening (Phases 8–10) — SHIPPED 2026-03-20</summary>

- [x] **Phase 8: High Availability** — Postgres advisory lock leader election with shared checkpoint store and automatic standby takeover (completed 2026-03-17)
- [x] **Phase 9: MongoDB Connector** — Change Streams consumption, BSON normalization, resume token persistence, and re-snapshot on token expiry (completed 2026-03-17)
- [x] **Phase 9.1: MongoDB HA Guard** [INSERTED] — Guard against passing MongoDB URI to Postgres HA election; INT-03 gap closure (completed 2026-03-17)
- [x] **Phase 10: Rust FFI Acceleration** — Optional Rust-accelerated pgoutput decoding, TOAST cache, and JSON serialization behind build tag (completed 2026-03-17)

Full archive: `.planning/milestones/v1.1-ROADMAP.md`

</details>

<details>
<summary>✅ v1.2 Benchmark Suite (Phases 11–13) — SHIPPED 2026-03-21</summary>

- [x] **Phase 11: Harness and Load Generator** — Docker Compose with all CDC tools against shared Postgres, plus loadgen binary with scenario modes (completed 2026-03-21)
- [x] **Phase 12: Metrics Collector and Scenarios** — Per-tool adapters writing to JSONL, all 5 benchmark scenarios executed (completed 2026-03-21)
- [x] **Phase 13: Report Generator** — Self-contained HTML report with charts and Markdown summary from JSONL data (completed 2026-03-21)

Full archive: `.planning/milestones/v1.2-ROADMAP.md`

</details>

<details>
<summary>✅ v2.0 Distributed Architecture (Phases 14–18) — SHIPPED 2026-05-03</summary>

- [x] **Phase 14: Shared State Foundation** — Shared Postgres cursor + backfill stores behind --cluster; cluster membership table with heartbeat-based node liveness (completed 2026-04-27)
- [x] **Phase 15: Distributed Event Log** — NATS JetStream embedded event log replacing node-local Badger; CHK-01 cluster-wide; pure Go binary preserved (completed 2026-04-28)
- [x] **Phase 16: Partition Ownership and Active/Active Delivery** — 64-partition ownership with atomic claim/steal/release; epoch fencing for zombie nodes; N-node active SSE/gRPC delivery (completed 2026-04-30)
- [x] **Phase 17: Distributed Source Coordination** — NATS KV WAL leader election with epoch fencing; MongoDB resume tokens in shared PostgresStore (completed 2026-04-30)
- [x] **Phase 18: MongoDB Cluster Infrastructure Wiring** [GAP-CLOSURE] — heartbeater.Run + pm.Run wired into runMongoPipeline errgroups; dead staleThreshold field and partition_assignments column removed (completed 2026-05-02)

Full archive: `.planning/milestones/v2.0-ROADMAP.md`

</details>

### 🚧 v2.1 Queue Sinks (In Progress)

**Milestone Goal:** Enable Kaptanto to publish CDC events directly to the major message queues — SQS, RabbitMQ, Kafka, Google Pub/Sub, and NATS — as output sinks with per-event push delivery, at-least-once guarantees, per-key ordering, and full observability.

- [x] **Phase 19: Sink Infrastructure and NATS Sink** — `sinks:` YAML config block, CLI flags, per-sink metrics and /healthz hooks, NATSSinkConsumer with JetStream at-least-once delivery (completed 2026-05-03)
- [x] **Phase 20: SQS Sink** — SQSConsumer with FIFO queue validation, MessageGroupId from primary key, IdempotencyKey as dedup attribute (completed 2026-05-04)
- [ ] **Phase 21: Kafka Sink** — KafkaConsumer using franz-go (CGO-free mandatory), record key from primary key, SASL/TLS auth
- [ ] **Phase 22: Google Pub/Sub Sink** — PubSubConsumer with ordering key, synchronous result.Get confirmation, ResumePublish on ordering-key errors
- [ ] **Phase 23: RabbitMQ Sink** — RabbitMQConsumer with per-partition channel pool, publisher confirms, and explicit reconnect loop

## Phase Details

### Phase 11: Harness and Load Generator
**Goal**: Anyone can start the full benchmark harness with one command and generate configurable load against it
**Depends on**: Phase 10 (Kaptanto binary exists and is buildable)
**Requirements**: HRN-01, HRN-02, HRN-03, HRN-04, LOAD-01, LOAD-02, LOAD-03
**Success Criteria** (what must be TRUE):
  1. `docker compose up` in `bench/` starts Kaptanto, Debezium Server, Sequin, PeerDB, and Postgres — all services reach healthy state within 2 minutes
  2. Kaptanto service is built from source via `Dockerfile.bench` (not a pre-built image); the compose service depends on the build completing
  3. `bench/cmd/loadgen` inserts rows at configurable rates (default 10k, up to 50k ops/s), with each row containing a `_bench_ts` column from `clock_timestamp()`
  4. Load generator accepts `--mode steady|burst|large-batch|idle` and executes the correct load shape for each mode
  5. Tool versions are pinned in `docker-compose.yml`; `bench/README.md` documents Maxwell's Daemon exclusion with the issue reference
**Plans**: 3 plans

Plans:
- [ ] 11-01: Docker Compose harness — compose file with all services, healthchecks, depends_on ordering, and Dockerfile.bench
- [x] 11-02: Load generator binary — `bench/cmd/loadgen` with configurable rate, `_bench_ts` column, scenario modes (completed 2026-03-21)
- [ ] 11-03: Harness integration — verify compose+loadgen end-to-end, pin versions, write bench/README.md

### Phase 12: Metrics Collector and Scenarios
**Goal**: All five benchmark scenarios run to completion and every CDC event from every tool is captured with end-to-end timing data
**Depends on**: Phase 11
**Requirements**: MET-01, MET-02, MET-03, MET-04, SCN-01, SCN-02, SCN-03, SCN-04, SCN-05
**Success Criteria** (what must be TRUE):
  1. Running the scenario orchestrator executes all 5 scenarios in sequence (steady, burst, large-batch, crash+recovery, idle) and produces `metrics.jsonl`
  2. Each line in `metrics.jsonl` contains tool name, scenario, receive timestamp, `_bench_ts` from payload, and computed latency in microseconds
  3. Per-tool adapters receive events correctly: Kaptanto via SSE, Debezium Server via HTTP POST webhook, Sequin via HTTP push, PeerDB via Kafka
  4. `docker_stats.jsonl` contains per-container CPU% and RSS (read from `/proc/1/status` VmRSS) sampled every 2 seconds throughout all scenarios
  5. Crash+recovery scenario (SCN-04) SIGKILLs each tool and records seconds until event delivery resumes
**Plans**: 3 plans

Plans:
- [ ] 12-01: Metrics collector — `bench/cmd/collector` with per-tool adapters (SSE, webhook, HTTP push, Kafka) writing to `metrics.jsonl`
- [ ] 12-02: Docker stats poller — `/proc/1/status` VmRSS reader writing to `docker_stats.jsonl` every 2s
- [ ] 12-03: Scenario orchestrator — steady, burst, large-batch, crash+recovery, idle scenarios with collector integration

### Phase 13: Report Generator
**Goal**: A single command turns raw JSONL data into a self-contained, shareable benchmark report with charts
**Depends on**: Phase 12
**Requirements**: RPT-01, RPT-02, RPT-03, RPT-04
**Success Criteria** (what must be TRUE):
  1. `bench/cmd/reporter` reads `metrics.jsonl` and `docker_stats.jsonl` and writes a single HTML file with all JS and CSS inlined (no CDN requests, works offline)
  2. HTML report contains charts for throughput, latency (p50/p95/p99), CPU%, RSS, and recovery time — one chart per scenario per metric
  3. HTML includes a methodology section covering tool versions, hardware specs, scenario definitions, measurement approach, and Maxwell's Daemon exclusion rationale
  4. `bench/results/REPORT.md` is generated alongside the HTML file, containing Markdown tables of results and a link to the HTML report
**Plans**: 2 plans

Plans:
- [ ] 13-01: Reporter binary — `bench/cmd/reporter` reads JSONL, computes percentiles, generates data structures for charts
- [ ] 13-02: HTML + Markdown output — self-contained HTML with inlined chart library, methodology section, and REPORT.md generation

### Phase 14: Shared State Foundation
**Goal**: Consumer delivery positions, backfill progress, and cluster membership are persisted in shared Postgres so any surviving node can resume any other node's work without gaps
**Depends on**: Phase 13
**Requirements**: STATE-01, STATE-02, STATE-03
**Success Criteria** (what must be TRUE):
  1. When a node crashes mid-delivery, a surviving node resumes SSE and gRPC delivery from the exact last acknowledged cursor position — no events are skipped and no already-delivered events are re-sent
  2. The `kaptanto_nodes` table shows all active nodes with their last heartbeat; a node that stops heartbeating is marked stale within one heartbeat interval and its partition assignments are released
  3. A backfill running on one node can be interrupted by killing that node; a different node starts `kaptanto` and the snapshot resumes from the last committed keyset cursor without restarting from scratch
  4. `make test` passes with CGO_ENABLED=0 — no new CGO dependencies introduced in this phase
**Plans**: 3 plans

Plans:
- [ ] 14-01-PLAN.md — PostgresCursorStore and --cluster config fields (STATE-01)
- [ ] 14-02-PLAN.md — PostgresBackfillStore and NodeHeartbeater (STATE-02, STATE-03)
- [ ] 14-03-PLAN.md — Wire Postgres stores into root.go behind --cluster flag (STATE-01, STATE-02, STATE-03)

### Phase 15: Distributed Event Log
**Goal**: The event log is Raft-replicated across all cluster nodes so any single node failure does not lose events, and CHK-01 holds cluster-wide
**Depends on**: Phase 14
**Requirements**: EVLOG-01, EVLOG-02, EVLOG-03
**Success Criteria** (what must be TRUE):
  1. Killing one node in a running 3-node cluster does not lose any events that were already appended — the cluster continues serving events from the replicated log
  2. The source LSN does not advance (confirmed_flush_lsn is not updated) until a quorum of NATS JetStream nodes confirms the append is durable — CHK-01 holds cluster-wide
  3. `make build` and `make test` succeed with CGO_ENABLED=0 — the Kaptanto binary remains pure Go; NATS runs as a co-located sidecar process started by `kaptanto start --cluster`
  4. A 3-node cluster can be started with a single `kaptanto start --cluster` invocation on each node — no separate NATS configuration steps required
**Plans**: 2 plans

Plans:
- [ ] 15-01-PLAN.md — NatsEventLog implementation, embedded server helper, unit tests (EVLOG-01, EVLOG-02)
- [ ] 15-02-PLAN.md — Config fields (ClusterPeers, NatsClusterPort), CLI flags, root.go wiring (EVLOG-03)

### Phase 16: Partition Ownership and Active/Active Delivery
**Goal**: Multiple active Kaptanto nodes each own a non-overlapping set of partitions and serve consumers concurrently, with per-key ordering preserved across all node join and leave events
**Depends on**: Phase 15
**Requirements**: DLVR-01, DLVR-02, DLVR-03, DLVR-04
**Success Criteria** (what must be TRUE):
  1. A new node joining a running cluster automatically claims unclaimed partitions and begins serving SSE and gRPC consumers for those partitions — no operator intervention required
  2. When a node leaves gracefully or is killed, its partitions are reassigned to surviving nodes; the old node drains all in-flight events before the new node begins consuming, and a zombie node that reconnects after being replaced cannot write events or advance cursors
  3. N Kaptanto nodes simultaneously serve SSE and gRPC consumers, each node serving only its owned partitions — consumers connected to any node receive events without gaps or duplicates
  4. Events for any given primary key arrive at downstream consumers in LSN order across node join, graceful leave, and crash-leave events — RTR-04 is not violated during partition reassignment
**Plans**: 3 plans

Plans:
- [ ] 16-01-PLAN.md — PartitionStore: kaptanto_partitions schema, atomic claim/steal/release operations (DLVR-01, DLVR-02)
- [ ] 16-02-PLAN.md — PartitionManager loop, epochCursorStore adapter, Router.SetOwnedPartitions patch (DLVR-01, DLVR-02, DLVR-03, DLVR-04)
- [ ] 16-03-PLAN.md — root.go wiring: PartitionManager + epochCursorStore behind --cluster, correct shutdown ordering (DLVR-01, DLVR-02, DLVR-03, DLVR-04)

### Phase 17: Distributed Source Coordination
**Goal**: The WAL leader is protected by NATS JetStream KV-backed election with epoch fencing so no zombie node can corrupt the replication slot, and MongoDB resume token progress survives node loss
**Depends on**: Phase 16
**Requirements**: SRCC-01, SRCC-02, SRCC-03
**Success Criteria** (what must be TRUE):
  1. A node that was network-partitioned and then reconnects after a new WAL leader was elected cannot advance the Postgres replication slot LSN or write events — epoch fencing tokens reject its operations
  2. Leader election does not require a separate coordination service — NATS JetStream KV (already embedded from Phase 15) provides atomic kv.Create consensus; any single node failure does not prevent a new leader from being elected
  3. When a MongoDB-sourced node crashes, the replacement node resumes the Change Stream from the correct position recorded in the shared store — no events already logged are re-processed, and no events between the last token and the crash are lost
**Plans**: 3 plans

Plans:
- [ ] 17-01-PLAN.md — NatsEventLog.Conn() accessor + WalLeaderElector (NATS KV TTL lease) with tests (SRCC-02)
- [ ] 17-02-PLAN.md — PostgresConnector epoch fencing: epochGetter field + SetEpochGetter + fenced sendStandbyStatus (SRCC-01)
- [ ] 17-03-PLAN.md — root.go wiring: WalLeaderElector into Postgres pipeline + PostgresStore ckStore for MongoDB cluster mode (SRCC-01, SRCC-02, SRCC-03)

### Phase 18: MongoDB Cluster Infrastructure Wiring [GAP-CLOSURE]
**Goal**: MongoDB+cluster deployments start the same cluster infrastructure goroutines (heartbeater, pm) as Postgres+cluster, fully closing STATE-02 and DLVR-03
**Depends on**: Phase 17
**Requirements**: STATE-02, DLVR-01, DLVR-02, DLVR-03
**Root cause:** `runMongoPipeline` is dispatched at root.go:566 before the errgroup block at 641–651, so `heartbeater.Run` and `pm.Run` are never started for MongoDB+cluster.
**Success Criteria** (what must be TRUE):
  1. In MongoDB+cluster mode, `heartbeater.Run` and `pm.Run` are started — `kaptanto_nodes` row is inserted and stale node detection fires
  2. `epochCursorStore.SaveCursor` correctly persists cursor positions for MongoDB+cluster consumers — partition ownership is respected, no silent drops
  3. `pm.ReleaseAll` is called on clean shutdown for MongoDB+cluster — partition rows are released and not left claimed
  4. `walElector` is nil for MongoDB+cluster (no WAL leader needed); inaccurate root.go comment is corrected
  5. Dead `NodeHeartbeater.staleThreshold` field and dead `kaptanto_nodes.partition_assignments` JSONB column are removed
  6. All tests pass; `make verify-no-cgo` green
**Plans**: 2 plans

Plans:
- [ ] 18-01-PLAN.md — Pass heartbeater and pm into runMongoPipeline; start cluster goroutines; call pm.ReleaseAll on shutdown (STATE-02, DLVR-01, DLVR-02, DLVR-03)
- [ ] 18-02-PLAN.md — Fix walElector nil guard for MongoDB+cluster; fix inaccurate root.go comment; remove dead staleThreshold field and partition_assignments column

### Phase 19: Sink Infrastructure and NATS Sink
**Goal**: Users can configure and run a queue sink output, with the full config/CLI/metrics/healthz framework in place and NATS JetStream validated as the first working sink
**Depends on**: Phase 18
**Requirements**: CFG-01, CFG-02, CFG-03, CFG-04, DLV-01, DLV-02, DLV-03, DLV-04, OBS-01, OBS-02, SNK-05
**Success Criteria** (what must be TRUE):
  1. User can add a `sinks:` block to `kaptanto.yaml` with NATS connection params, TLS settings, and a Go template for subject routing (e.g., `cdc.{{.Schema}}.{{.Table}}`), and start Kaptanto with `--output nats` — events are published to the configured NATS JetStream subject
  2. Every published event's `IdempotencyKey` is included as a NATS message header, and `Deliver` blocks until the JetStream server returns an `PubAck` — cursor does not advance before the broker confirms receipt (CHK-01 preserved)
  3. Transient NATS broker errors (connection drops, timeout) trigger automatic retry via `RetryScheduler` without crashing the pipeline — the sink recovers and resumes delivery
  4. Prometheus metrics `queue_publish_total`, `queue_publish_errors_total`, and `queue_publish_latency_seconds` are populated for the active NATS sink and visible at `/metrics`
  5. `/healthz` includes a `nats` probe that reports unhealthy when the NATS connection is down
**Plans**: 3 plans

Plans:
- [ ] 19-01-PLAN.md — Config types (SinksConfig, NATSSinkConfig, TLSConfig) + queue publish metrics
- [ ] 19-02-PLAN.md — NATSSinkConsumer implementation (Deliver, Ping, TLS, stream validation)
- [ ] 19-03-PLAN.md — root.go wiring: case "nats":, health probe, obs HTTP server

### Phase 20: SQS Sink
**Goal**: Users can publish CDC events to an AWS SQS FIFO queue with per-key ordering preserved end-to-end via MessageGroupId
**Depends on**: Phase 19
**Requirements**: SNK-01
**Success Criteria** (what must be TRUE):
  1. User can configure `--output sqs` with an SQS FIFO queue URL, region, and AWS credentials (IAM role, environment variables, or static keys) — Kaptanto starts and publishes events to the queue
  2. Kaptanto detects at startup if the configured queue is a Standard (non-FIFO) queue and exits with a clear error message — Standard queues are rejected because they cannot preserve per-key ordering
  3. Each published message has `MessageGroupId` set to the event's primary key hash, `MessageDeduplicationId` set to the `IdempotencyKey`, and the raw `IdempotencyKey` value in a message attribute — downstream consumers can deduplicate without parsing the body
  4. `make build CGO_ENABLED=0` succeeds with the SQS sink included — no CGO introduced
**Plans**: 3 plans

Plans:
- [ ] 20-01-PLAN.md — SQSSinkConfig type + aws-sdk-go-v2 module installation
- [ ] 20-02-PLAN.md — SQSSinkConsumer implementation (Deliver, Ping, FIFO validation, interface injection for tests)
- [ ] 20-03-PLAN.md — root.go wiring: case "sqs":, health probe, obs HTTP server, cmd tests

### Phase 21: Kafka Sink
**Goal**: Users can publish CDC events to a Kafka topic with per-key ordering preserved via record key, using a pure-Go client that satisfies the CGO_ENABLED=0 build constraint
**Depends on**: Phase 20
**Requirements**: SNK-03
**Success Criteria** (what must be TRUE):
  1. User can configure `--output kafka` with bootstrap brokers, topic template, and optional SASL (PLAIN/SCRAM-SHA-256/SCRAM-SHA-512) and TLS settings — Kaptanto starts and produces events to the configured Kafka topic
  2. Each Kafka record's key is set to the CDC event's primary key value — consumers relying on Kafka's partition-by-key guarantee receive events for the same database row on the same partition in order
  3. `make build CGO_ENABLED=0` and `make verify-no-cgo` succeed — franz-go is used exclusively; confluent-kafka-go is not present in go.mod
  4. `make test CGO_ENABLED=0` passes for the Kafka sink unit tests
**Plans**: TBD

### Phase 22: Google Pub/Sub Sink
**Goal**: Users can publish CDC events to a Google Pub/Sub topic with per-key ordering preserved and correct ResumePublish recovery after ordering-key errors
**Depends on**: Phase 21
**Requirements**: SNK-04
**Success Criteria** (what must be TRUE):
  1. User can configure `--output pubsub` with a GCP project ID, topic ID, and credentials (Application Default Credentials or explicit service account key path) — Kaptanto starts and publishes events to the Pub/Sub topic
  2. Each published message has its ordering key set to the CDC event's primary key, and `Publish().Get(ctx)` is called before `Deliver` returns nil — cursor does not advance until the Pub/Sub server confirms the message is durably accepted
  3. When a Pub/Sub publish fails for an ordering key, `topic.ResumePublish(orderingKey)` is called before retrying — delivery for all primary keys resumes without operator intervention after a transient broker error
  4. `make build CGO_ENABLED=0` succeeds with the Pub/Sub sink included — no CGO introduced
**Plans**: TBD

### Phase 23: RabbitMQ Sink
**Goal**: Users can publish CDC events to a RabbitMQ exchange via AMQP with publisher confirms, concurrent-safe per-partition channel pool, and automatic reconnect on connection loss
**Depends on**: Phase 22
**Requirements**: SNK-02
**Success Criteria** (what must be TRUE):
  1. User can configure `--output rabbitmq` with an AMQP URL (including optional TLS), exchange name, and routing key template — Kaptanto starts and publishes events to the configured RabbitMQ exchange
  2. `Deliver` blocks until the broker sends a publisher confirm (`ack`) for the published message — cursor does not advance before the broker confirms receipt; `nack` triggers retry via `RetryScheduler`
  3. When the AMQP connection is lost (broker restart, network interruption), the sink automatically re-dials with exponential backoff and resumes publishing without crashing the pipeline — events during reconnect are retried, not dropped
  4. Concurrent `Deliver` calls from the router's 64 partition goroutines do not corrupt channel state — each partition uses a dedicated AMQP channel (per-partition channel pool)
  5. `make build CGO_ENABLED=0` succeeds with the RabbitMQ sink included — no CGO introduced
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
| 8. High Availability | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 9. MongoDB Connector | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 9.1. MongoDB HA Guard [INSERTED] | v1.1 | 1/1 | ✓ Complete | 2026-03-17 |
| 10. Rust FFI Acceleration | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 11. Harness and Load Generator | v1.2 | 3/3 | ✓ Complete | 2026-03-21 |
| 12. Metrics Collector and Scenarios | v1.2 | 3/3 | ✓ Complete | 2026-03-21 |
| 13. Report Generator | v1.2 | 2/2 | ✓ Complete | 2026-03-21 |
| 14. Shared State Foundation | v2.0 | 3/3 | ✓ Complete | 2026-04-28 |
| 15. Distributed Event Log | v2.0 | 2/2 | ✓ Complete | 2026-04-28 |
| 16. Partition Ownership and Active/Active Delivery | v2.0 | 3/3 | ✓ Complete | 2026-04-30 |
| 17. Distributed Source Coordination | v2.0 | 3/3 | ✓ Complete | 2026-05-01 |
| 18. MongoDB Cluster Infrastructure Wiring [GAP] | v2.0 | 2/2 | ✓ Complete | 2026-05-02 |
| 19. Sink Infrastructure and NATS Sink | v2.1 | 3/3 | ✓ Complete | 2026-05-04 |
| 20. SQS Sink | 3/3 | Complete   | 2026-05-04 | - |
| 21. Kafka Sink | v2.1 | 0/TBD | Not started | - |
| 22. Google Pub/Sub Sink | v2.1 | 0/TBD | Not started | - |
| 23. RabbitMQ Sink | v2.1 | 0/TBD | Not started | - |
