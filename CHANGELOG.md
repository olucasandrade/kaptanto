# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
