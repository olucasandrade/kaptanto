# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It streams changes from Postgres (WAL logical replication) and MongoDB (Change Streams) to stdout, SSE, gRPC, or one of five message-broker sinks (Kafka, NATS, SQS, Google Pub/Sub, RabbitMQ). The name means "who captures" in Esperanto.

The implementation is complete. `kaptanto-technical-specification.md` remains the authoritative architecture reference.

## Build, Lint & Test

```bash
make build             # CGO_ENABLED=0 static binary (default, cross-platform)
make lint               # golangci-lint over ./internal/... ./cmd/... (config: .golangci.yml)
make test               # all tests, CGO_ENABLED=0
make test-race          # race detector (requires CGO)
make cover               # coverage run; fails if below COVERAGE_THRESHOLD (default 50.0%)
make verify-no-cgo      # cross-compile linux/amd64 + darwin/arm64 to confirm no CGO leakage
make build-rust         # optional Rust-accelerated binary (requires Rust 1.77+, cargo, cbindgen)
```

Run a single test:
```bash
go test ./internal/router -run TestPerKeyOrdering -v
go test ./internal/cmd -run TestFlagSource -v
```

Env-gated / slower suites (not run by default):
```bash
POSTGRES_TEST_DSN=... MONGO_TEST_URI=... make test-integration   # live Postgres + MongoDB
POSTGRES_TEST_DSN=... make test-e2e                              # black-box binary tests, -tags e2e
make mutation                                                    # gremlins over router/eventlog/parser/backfill (.gremlins.yaml)
```

Pure Go build (`CGO_ENABLED=0`) is enforced for static distribution. The Rust FFI path requires the `build_ffi`/`rust` build tag and CGO; it's host-only, not cross-compilable.

## Architecture

### Data Flow

```
Source (Postgres WAL / MongoDB Change Stream)
  → Parser (pgoutput/parser.go or mongodb/normalizer.go)
      → EventLog (badger.go, 64 partitions, TTL, dedup by IdempotencyKey)
          → Checkpoint saved (ONLY after Append succeeds — CHK-01)
              → Router (fan-out to consumers, per-key ordering — RTR-04)
                  → Output: stdout NDJSON / SSE `/events` / gRPC CdcStream /
                    Kafka / NATS / SQS / Google Pub/Sub / RabbitMQ
```

Every output in `internal/output/` implements `router.Consumer`. The five broker sinks (Kafka/NATS/SQS/PubSub/RabbitMQ) follow the same durability contract as the direct outputs: the router's cursor only advances after the broker acknowledges the write, per-key partition/routing-key selection preserves ordering, and no sink retries internally — retry is `internal/router/retry.go`'s job. Each sink has its own connection block under `Config.Sinks` (`internal/config/config.go`).

Backfill runs concurrently with WAL streaming. The WatermarkChecker discards snapshot rows where a WAL event with a higher LSN already exists for the same key (same 64-partition hash as EventLog — BKF-02).

### Key Packages

| Package | Role |
|---|---|
| `internal/cmd/root.go` | Cobra CLI, pipeline assembly, graceful shutdown |
| `internal/event/event.go` | ChangeEvent struct (ULID ID, unified insert/update/delete/read/control) |
| `internal/config/config.go` | YAML + CLI flag merging; CLI flags always win; sink/TLS/HA/cluster config structs |
| `internal/eventlog/badger.go` | Durable append-only store: FNV-1a partitioned, TTL, seq=0 on dup |
| `internal/source/postgres/connector.go` | Logical replication slot, heartbeats, reconnect backoff |
| `internal/source/mongodb/connector.go` | Change Streams, resume token, snapshot on InvalidResumeToken |
| `internal/parser/pgoutput/parser.go` | WAL → ChangeEvent; RelationCache + TOASTCache |
| `internal/backfill/backfill.go` | Snapshot engine (keyset cursor, watermark check) |
| `internal/router/router.go` | Fan-out, per-key ordering, cursor persistence; retry logic in `retry.go` |
| `internal/output/sse/server.go` | SSE `/events` endpoint with consumer/table/operation filters |
| `internal/output/grpc/server.go` | gRPC Subscribe + Acknowledge RPCs |
| `internal/output/{kafka,nats,sqs,pubsub,rabbitmq}` | Broker sinks, each a `router.Consumer`; see `Config.Sinks` for connection settings |
| `internal/ha/leader.go` | Postgres advisory lock leader election (~5s failover) |
| `internal/observability/metrics.go` | Custom prometheus.Registry; `/metrics` + `/healthz` |
| `internal/checkpoint/` | SQLite (local) or PostgreSQL (HA) for source LSN + consumer cursors |
| `internal/action/` | Action registry: type definitions (ACT-01), param validation (ACT-02), routing match integration |
| `internal/routing/` | Compiled match-rule evaluation: glob tables, bitmask ops, WHERE filters (RTG-01, RTG-02) |
| `internal/openapi/` | `/openapi.json` generator: reflects configured actions into a byte-stable OpenAPI 3.1 spec (OAS-01) |
| `packages/kaptanto-events/` | TypeScript SDK: typed SSE client, `ChangeEvent` types, auto-reconnect |
| `n8n-nodes-kaptanto/` | n8n community node: SSE trigger node with table/operation/consumer filters |

### Runtime Data Directory

```
./data/
├── events/        # Badger event log
├── checkpoint.db  # SQLite: source LSN (non-HA)
├── cursors.db     # SQLite: per-consumer, per-partition delivery positions
└── backfill.db    # SQLite: snapshot progress + watermark state
```

## Critical Invariants

These must never be violated. This list mirrors the codebase's headline invariants; a repo-wide `grep -rhoE '[A-Z]{3}-[0-9]{2}' internal/` turns up further per-package codes (`BKF-`, `CFG-`, `EVT-`, `RCC-`, `SRC-`, `OUT-`, `LOG-`, etc.) that refine these same guarantees for specific components.

1. **CHK-01 — Durability:** Source checkpoint NEVER advances until `EventLog.Append()` returns successfully. Crash → source re-sends → EventLog deduplicates by `IdempotencyKey`.

2. **RTR-04 — Per-key ordering:** Router delivers events for the same primary key in order. A failed delivery blocks that key only; other keys continue. Retry logic in `internal/router/retry.go`.

3. **BKF-02 — Watermark consistency:** WatermarkChecker and EventLog must use the same partition count (64) so FNV-1a hashes are consistent.

4. **TOAST handling:** Postgres UPDATE events may omit unchanged large columns. Parser merges from TOASTCache keyed by `(relation_id, primary_key)`.

5. **Keyset cursors, never OFFSET:** Snapshot pagination uses `internal/backfill/cursor.go`; OFFSET breaks under concurrent writes.

6. **SRC-01 — Connection isolation:** Postgres connector keeps a separate `pgx.Conn` for snapshots; replication connections cannot be reused for regular queries.

7. **DLV-02/03/04 — Broker sink delivery:** Sinks route by CDC key to preserve per-key ordering (DLV-02), never retry internally (DLV-03 — retry is the router's job), and stamp every message with an idempotency key/header for downstream dedup (DLV-04). See `internal/output/kafka/consumer.go` for the reference implementation.

8. **ACT-01 — Types are data only:** An action type is a function from (params, secrets) to a webhook sink config + transform expression. Types must not add delivery code paths; every byte goes through the webhook sink.

9. **ACT-02 — Secret params:** Secret params (`ParamSpec.Secret = true`) must be `${VAR}` env-var references. They are never logged, never included in the OpenAPI spec, and never embedded as literals in config.

10. **ACT-03 — Non-matching events:** Events that do not match any action's routing rule are acknowledged and the cursor advances. They are not retried, queued, or dead-lettered.

11. **RTG-01 — Rules compile at startup:** `routing.Compile()` is called once at startup for each action's `match:` block. Any validation error aborts the process.

12. **RTG-02 — No-alloc match:** `Matcher.Match()` performs no heap allocation on the miss path. 50 compiled matchers evaluate in < 1ms.

13. **OAS-01 — Byte-stable spec:** The OpenAPI JSON output is deterministic — same config produces byte-identical output across runs (ordered maps, stable marshal).

## Test Patterns

- Tests use fake implementations (e.g., `fakeConsumer`, `fakeEventLog`) rather than mocks.
- `internal/metrics`: each test creates its own `prometheus.Registry` via `NewKaptantoMetrics()` — no global state.
- `internal/cmd`: tests call `cmd.ExecuteWithArgs(args, out)` which creates a fresh `cobra.Command` — no shared state.
- Router tests pass `nil` for `cursorStore`; the router substitutes a `noopCursorStore` automatically.

## Benchmarking

The `bench/` directory contains a harness that compares Kaptanto vs Debezium, Sequin, and PeerDB. It requires Docker Compose.

```bash
cd bench
docker compose down -v          # REQUIRED before every run (prevents cross-run state contamination)
docker compose up --build -d
# Run a scenario:
go run ./cmd/scenarios -- --scenario steady
docker compose down -v          # clean up after
```

Results are written to `bench/results/`. The rendered report is at `bench/results/REPORT.md`.

Clean-run procedure is mandatory — see memory for details on contamination risk.

## Configuration

YAML config (all fields also available as CLI flags; flags take precedence):

```yaml
source: "postgres://user:pass@host/db"
output: sse          # stdout | sse | grpc | kafka | nats | sqs | pubsub | rabbitmq
port: 7654
data_dir: /var/lib/kaptanto
retention: 24h
ha: false             # CFG-01: enable leader election (Postgres advisory lock)
node_id: ""           # CFG-01: node identity when ha is enabled
cluster: false        # shared cursor state (PostgresCursorStore) across nodes
cluster_dsn: ""       # Postgres DSN for shared cursor store when cluster is enabled

tables:
  - name: public.orders
    columns: [id, status, total]
    where: "status != 'archived'"

sinks:                # only the active sink's block needs to be populated
  kafka:
    brokers: ["localhost:9092"]
    topic-template: "cdc.{{.Schema}}.{{.Table}}"
```

`ServerTLS` (`server-tls` in YAML) enables TLS/mTLS for the inbound SSE/gRPC servers, distinct from per-sink outbound TLS. `auth-token` (or `KAPTANTO_AUTH_TOKEN` env var) sets a static bearer token for the SSE/gRPC data plane; `insecure: true` disables this with a loud startup warning and is not for production.

Actions config example (`output: none` runs kaptanto as a pure action processor):

```yaml
source: "postgres://user:pass@host/db"
output: none

actions:
  - name: order-alerts
    type: slack
    params:
      webhook-url: ${SLACK_WEBHOOK_URL}
    match:
      tables: ["public.orders"]
      operations: ["insert"]

  - name: sync-vectors
    type: vector-upsert
    params:
      api-key: ${PINECONE_API_KEY}
      index-host: my-index.svc.us-east1-gcp.pinecone.io
      vector-field: embedding
    match:
      tables: ["public.products"]
      operations: ["insert", "update"]
```

## Landing Page

`landing/` is a Qwik app. Run `npm run dev` from `landing/` for local development. Documentation content lives in `landing/src/data/docs-content.ts` as the `DOCS_CONTENT` record; the sidebar is the `SIDEBAR` array in the same file. SEO-crawlable routes are at `landing/src/routes/docs/`; the corresponding SEO metadata is in `landing/src/data/docs.ts`.
