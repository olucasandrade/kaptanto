# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-07-04

### Added

#### Data-plane security
- **Bearer-token authentication** for the SSE and gRPC data plane (`auth-token` config field or `KAPTANTO_AUTH_TOKEN` env var); enforced at startup for every network output, including sink-mode observability endpoints — `--insecure` opts out with a loud warning
- **Server-side TLS / mTLS** for the inbound SSE and gRPC servers (`server-tls` YAML block), distinct from per-sink outbound TLS; incomplete client-CA-only configs are rejected at startup
- Case-insensitive `Bearer` scheme handling; sink observability endpoints protected by the same middleware

#### Source safety
- Postgres source now **fails closed when no tables are configured**; capturing the whole database requires an explicit `--all-tables` opt-in

### Fixed

#### Router / delivery correctness
- Consumer cursor persists only up to the lowest blocked-group floor, and follow-on events for blocked message groups are queued and drained inline instead of dropped or re-read (RTR-04)
- `BatchFlusher` cursor advance deferred until the flush actually succeeds
- Delivery gated by each consumer's own cursor; pending buffer scoped per partition to prevent cross-partition data loss
- Retries delivered outside the scheduler lock

#### Backfill / snapshot correctness
- Snapshot discovers the table's real primary-key columns instead of assuming a column named `id`
- Schema, table, and PK identifiers quoted in keyset snapshot SQL
- Snapshot PK values normalized to WAL text form so watermark hashes match (BKF-02); watermark check pages through partitions instead of loading them whole
- Cursor advances from the last scanned PK; sends to a nil EventLog block instead of silently dropping rows

#### Parsing / key correctness
- Unique idempotency key per change within a transaction (previously collided)
- Primary-key canonicalization for `pgtype.Numeric`, `bytea`, UUID `[16]byte`, bool, and `timestamptz` values so WAL and snapshot forms agree
- Nil-tuple guards in pgoutput insert/update/delete handlers
- `--tables` splits on commas as documented

#### Security hardening
- DSN passwords — including query-parameter form — redacted from startup logs and HA errors
- Raw primary-key values and idempotency keys removed from delivery-failure and dead-letter logs
- Data directory created with `0700`; HTTP server timeouts set on SSE and observability endpoints; SSE CORS locked down by default with a configurable allowed origin

### Performance

- Raw-bytes passthrough on fan-out: events are no longer re-marshaled per consumer (EventLog → outputs)
- Router dispatch: 10 ms poll loop replaced with event-driven notify (500 ms fallback); per-event allocations eliminated
- Backfill: snapshot row appends batched to amortise fsync cost; watermark scan replaced with an indexed lookup (was O(rows × events)); per-row field/PK work hoisted out of the loop
- MongoDB: change-stream appends batched into the EventLog
- Queue sinks: publishes batched via the `BatchFlusher` interface; SSE consumer drops redundant cursor persistence; column-filter allow-set precomputed per consumer

### Infrastructure

- CI strictness: golangci-lint standard suite with gocyclo 25, dedicated race-detector job, 70% coverage gate with per-package floors, mutation-test efficacy ratchet at 60%
- Security scanning is now blocking: CodeQL, zizmor, cargo-audit; GitHub Actions SHA-pinned; dependency bumps for CVEs (`golang.org/x/net`, `pgx/v5`)
- New e2e crash/restart durability, backfill race, and SSE coverage; Rust FFI tests wired into CI; integration suite skip-detection canaries and RabbitMQ round-trip
- Landing page CI gate (lint/typecheck/build + smoke tests), site-wide security headers, live GitHub star count, benchmark report refreshed from the July 2026 clean runs
- Benchmark harness fixes: idempotent NATS stream pre-creation, statsd RSS collection, stack lifecycle owned by `cmd/scenarios`

## [0.2.0] - 2026-05-30

### Added

#### New sink outputs
- **NATS JetStream sink** (`--output nats`) — publishes CDC events to a NATS JetStream subject; configurable subject prefix and per-table routing
- **AWS SQS sink** (`--output sqs`) — delivers events to SQS FIFO queues with per-table `QueueURLTemplate` routing and a validated queue pool
- **Apache Kafka sink** (`--output kafka`) — produces events to Kafka topics via franz-go; per-table topic routing
- **GCP Pub/Sub sink** (`--output pubsub`) — publishes to Pub/Sub topics with lazy publisher pool and per-table topic routing
- **RabbitMQ sink** (`--output rabbitmq`) — AMQP 0-9-1 publish with publisher confirms and automatic reconnect loop

#### Sink hardening
- mTLS / TLS support for all sinks (`tls.ca_file`, `tls.cert_file`, `tls.key_file`)
- Per-table routing via `QueueURLTemplate` for SQS
- Sink-level output metrics (published events, errors, latency)

#### HA cluster mode (`--cluster`)
- **NATS-backed EventLog** — replaces local Badger with a distributed JetStream stream; embeds a NATS server per node
- **PartitionManager** — claims/steals/releases the 64 FNV-1a partitions across nodes using epoch fencing (SRCC-01)
- **Epoch fencing** — `WalLeaderElector` prevents split-brain WAL writes across epochs
- **NodeHeartbeater** — liveness heartbeat written to Postgres; stale nodes have partitions stolen
- **PostgresCursorStore / PostgresBackfillStore** — shared cursor and backfill state for multi-node deployments
- `--cluster-peers` and `--cluster-nats-port` CLI flags for peer discovery

#### Config & observability
- New YAML fields: `sinks.*` (all five sink types), `cluster.*`, `tls.*`
- Extended Prometheus metrics for sink outputs and cluster partition ownership
- `--output` flag usage string updated to list all 8 modes

#### Examples
- `examples/audit-trail` — event-sourced audit log
- `examples/cursor-resume` — consumer cursor persistence across restarts
- `examples/fanout` — broadcasting a single stream to multiple consumers

### Changed

- `internal/cmd/root.go` split into `filters.go`, `mongo.go`, and `output.go` for maintainability
- Landing page revamped: new component structure, realistic benchmark numbers, docs content, use-cases section, changelog timeline
- Existing examples refreshed to match current API

### Infrastructure

- **Benchmark harness** (`bench/`) — end-to-end comparison of kaptanto, Debezium, Sequin, PeerDB across five scenarios: `steady`, `burst`, `large-batch`, `crash-recovery`, `idle`
- Kafka and NATS sink collector adapters added to the benchmark harness
- Two-node cluster scenario (`--scenario cluster`) in the benchmark
- CI benchmark workflow (`benchmark.yml`) — runs on push to `main` and on release tags; uploads `REPORT.md` + raw metrics as a 90-day artifact
- Goreleaser publishes multi-platform binaries (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64) and Docker Hub images on every `v*` tag

## [0.1.0] - 2025-01-01

Initial release.

- Postgres WAL logical replication source (pgoutput)
- MongoDB Change Streams source
- stdout NDJSON, SSE (`/events`), and gRPC (`CdcStream`) outputs
- Durable Badger EventLog with 64-partition FNV-1a fan-out and TTL dedup
- Keyset-cursor snapshot backfill with WatermarkChecker
- SQLite checkpoints (source LSN + consumer cursors)
- Per-key delivery ordering in the Router (RTR-04)
- Postgres advisory-lock leader election (~5 s failover)
- Prometheus metrics + `/healthz` endpoint
- Single static CGO-free binary (`CGO_ENABLED=0`)
