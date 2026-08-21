# Kaptanto

Every insert, update, and delete from your Postgres or MongoDB database, streamed the moment it happens, as one static Go binary.

*"Kaptanto" means "who captures" in Esperanto.*

```bash
./kaptanto \
  --source "postgres://localhost:5432/mydb" \
  --tables public.orders,public.payments \
  --output stdout

{"op":"insert","table":"orders","after":{"id":1234,"status":"pending","total":99.99}}
{"op":"update","table":"orders","before":{"status":"pending"},"after":{"status":"shipped"}}
```

## How it works

Kaptanto tails your database's transaction log — Postgres WAL (logical replication) or MongoDB Change Streams — instead of polling tables. Each change is parsed into a unified `ChangeEvent`, durably appended to an embedded event log, and only then is the source checkpoint advanced, so a crash can never lose an event. An initial snapshot (backfill) runs concurrently with live streaming; a watermark check discards snapshot rows already superseded by a WAL event. Events are then fanned out, in per-key order, to any number of outputs.

![Kaptanto architecture](./docs/architecture/kaptanto_architecture.png)

## Features

- **Zero runtime dependencies** — static binary
- **Durable event log** — every event is written before the source checkpoint advances; a crash never loses an event
- **Ten outputs** — stdout (NDJSON), SSE, gRPC, Kafka, NATS, SQS, Google Pub/Sub, RabbitMQ, webhook, and vector (embeddings → pgvector / Pinecone / Qdrant)
- **AI-native** — MCP server for agents, fail-open `ai_context` enrichment, vector sink for RAG, plus TypeScript / Python / Mastra SDKs
- **Consistent backfills** — snapshot and live stream run concurrently; watermark coordination prevents duplicate or stale rows
- **Per-key ordering** — events for the same primary key always arrive in commit order, even across broker sinks
- **Per-consumer cursors** — each consumer tracks its own position; reconnect at any time and resume exactly where you left off
- **Filtering** — table, column, operation, and SQL `WHERE` condition filters
- **High availability** — leader election via Postgres advisory lock (`--ha`), ~5s failover; optional cluster mode (`--cluster`) shares consumer cursor state across nodes
- **Security** — TLS/mTLS and bearer-token auth on the SSE/gRPC data plane (`--insecure` to explicitly opt out)
- **Observability** — Prometheus metrics and health check on `--port` for SSE and queue sinks, `--port + 1` for gRPC (stdout serves no HTTP endpoints)

## Quick start

```bash
make build   # produces ./kaptanto
```

Enable logical replication in `postgresql.conf`:

```
wal_level = logical
```

Grant replication access:

```sql
CREATE ROLE kaptanto WITH REPLICATION LOGIN PASSWORD 'secret';
GRANT SELECT ON TABLE public.orders, public.payments TO kaptanto;
-- for full before/after values on updates and deletes:
ALTER TABLE public.orders REPLICA IDENTITY FULL;
```

Run:

```bash
./kaptanto \
  --source "postgres://kaptanto:secret@localhost:5432/mydb" \
  --tables public.orders,public.payments \
  --output stdout
```

### MongoDB

No server-side setup is required beyond a replica set (Change Streams need one, even a single-node
`rs0`). Point `--source` at a `mongodb://` or `mongodb+srv://` URI — Kaptanto detects the source
type from the DSN scheme:

```bash
./kaptanto \
  --source "mongodb://localhost:27017/mydb" \
  --tables mydb.orders,mydb.payments \
  --output stdout
```

If the resume token is invalidated (e.g. oplog rollover while disconnected), Kaptanto automatically
falls back to a fresh snapshot.

## Outputs

| Output | Description |
|---|---|
| `stdout` | One JSON line per event — pipe to `jq`, a log collector, or any stdin reader |
| `sse` | HTTP Server-Sent Events at `/events`; each client is an independent consumer with its own cursor (`?consumer=`, `?tables=`, `?operations=`) |
| `grpc` | Typed streaming via Protocol Buffers (`Subscribe` + `Acknowledge` RPCs) |
| `kafka` / `nats` / `sqs` / `pubsub` / `rabbitmq` | Message-broker sinks, each routed by CDC key to preserve ordering and stamped with an idempotency key for downstream dedup |
| `webhook` | HTTP POST sink (also the delivery path for Actions) |
| `vector` | Embed extracted text and upsert/delete vectors in pgvector, Pinecone, or Qdrant |

```bash
./kaptanto --source "..." --tables public.orders --output sse --port 7654
curl -N http://localhost:7654/events?consumer=worker-1
```

Every broker sink routes by CDC key (partition, subject, queue, or routing key), so per-key
ordering is preserved end to end; none retries internally — retry is the router's job — and every
message carries an idempotency key/header for downstream dedup.

| Sink | Routing | Notes |
|---|---|---|
| `kafka` | `topic-template` (Go template, e.g. `cdc.{{.Schema}}.{{.Table}}`), keyed partition | SASL (`PLAIN`, `SCRAM-SHA-256/512`) and TLS/mTLS supported |
| `nats` | `subject-template` | JetStream; optional `stream-name` validated at startup; supports its own cluster mode via `--cluster-peers`/`--nats-cluster-port` |
| `sqs` | `queue-url` or `queue-url-template` | Must be a FIFO queue (`.fifo`); uses static credentials or the standard AWS credential chain |
| `pubsub` | `topic-id` or `topic-template` | Uses Application Default Credentials unless `credentials-file` is set |
| `rabbitmq` | `routing-key-template`, optional `exchange` | AMQP or AMQPS (TLS) URL |
| `vector` | CDC key → stable vector ID; text from `source.columns` or `source.template` | OpenAI-compatible / Cohere embedders; stores: pgvector, Pinecone, Qdrant |

Seven connection blocks live under `sinks:` (`kafka`, `nats`, `sqs`, `pubsub`, `rabbitmq`, `webhook`, `vector`) — only the active output's block needs to be populated.

## Configuration

All flags are available via CLI or YAML; CLI flags always win.

```yaml
source: "postgres://kaptanto:secret@localhost:5432/mydb"
output: sse
port: 7654
cors-origin: ""          # SSE Access-Control-Allow-Origin; empty = no cross-origin browser access
data-dir: /var/lib/kaptanto
retention: 24h
ha: false                # leader election (Postgres advisory lock)
node-id: ""              # unique node identity, required when ha is enabled
source-id: default       # slot name kaptanto_<id> / publication kaptanto_pub_<id>
all-tables: false        # explicit opt-in to replicate every table when 'tables:' is empty
cluster: false           # shared cursor state across nodes
cluster-dsn: ""          # Postgres DSN for the shared cursor store, required when cluster is true
cluster-peers: []        # NATS JetStream cluster peer addresses, e.g. ["node2:6222", "node3:6222"]
nats-cluster-port: 6222

tables:
  public.orders:
    columns: [id, status, total]
    where: "status != 'archived'"
  public.payments: {}     # empty = replicate all columns, no row filter

sinks:                    # only the active sink's block needs to be populated
  kafka:
    bootstrap-servers: ["localhost:9092"]
    topic-template: "cdc.{{.Schema}}.{{.Table}}"
    sasl-mechanism: ""     # "", PLAIN, SCRAM-SHA-256, or SCRAM-SHA-512
    tls: { ca-file: "", cert-file: "", key-file: "" }

server-tls:               # inbound TLS for the SSE/gRPC server, distinct from sink TLS above
  cert-file: ""
  key-file: ""
  client-ca-file: ""       # set to require + verify client certs (mTLS)

auth-token: ""             # bearer token for SSE/gRPC (prefer KAPTANTO_AUTH_TOKEN env var)
insecure: false            # explicit opt-out of TLS/auth — not for production
```

| Flag | Default | Description |
|---|---|---|
| `--source` | (required) | Database connection string |
| `--tables` | (required unless `--all-tables`) | Tables to replicate, e.g. `public.orders,public.users` |
| `--all-tables` | `false` | Explicit opt-in to capture every table when no `--tables`/`tables:` given |
| `--config` | | Path to YAML config file (flags still take precedence) |
| `--output` | `stdout` | `stdout`, `sse`, `grpc`, `kafka`, `nats`, `sqs`, `pubsub`, `rabbitmq`, `webhook`, `vector`, `none` |
| `--port` | `7654` | TCP port for the SSE/gRPC server (metrics/health at `port + 1`) |
| `--cors-origin` | | SSE `Access-Control-Allow-Origin` value; empty sends no CORS header |
| `--data-dir` | `./data` | Directory for the event log, checkpoint, cursor, and backfill stores |
| `--retention` | `0` (→ `1h`) | Event log TTL, e.g. `24h`, `7d` |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--ha` | `false` | Enable leader election via Postgres advisory lock |
| `--node-id` | | Unique node identity, required when `--ha` is set |
| `--source-id` | `default` | Logical source name; determines the replication slot/publication name |
| `--cluster` | `false` | Enable shared cursor state across nodes |
| `--cluster-dsn` | | Postgres DSN for the shared cursor store; required when `--cluster` is set |
| `--cluster-peers` | | NATS JetStream cluster peer addresses; required for a clustered NATS sink |
| `--nats-cluster-port` | `6222` | NATS JetStream cluster route port for this node |
| `--tls-cert` / `--tls-key` | | Server certificate/key PEM for the SSE/gRPC server |
| `--tls-client-ca` | | CA PEM to require and verify client certs (mTLS) |
| `--auth-token` | | Bearer token for the SSE/gRPC data plane (or `KAPTANTO_AUTH_TOKEN` env var) |
| `--insecure` | `false` | Explicitly disable data-plane TLS/auth — not for production |

## Actions

Actions turn CDC events into side effects — Slack messages, HTTP requests, email alerts, cache purges, vector upserts, or workflow triggers — without writing consumer code. Set `output: none` to run kaptanto as a pure action processor.

```yaml
source: "postgres://localhost:5432/mydb"
output: none

actions:
  - name: order-alerts
    type: slack
    params:
      webhook-url: ${SLACK_WEBHOOK_URL}
    match:
      tables: ["public.orders"]
      operations: ["insert"]
```

### Action types

| Type | Key params | Description |
|---|---|---|
| `slack` | `webhook-url` (secret) | Post to a Slack incoming webhook |
| `discord` | `webhook-url` (secret) | Post to a Discord webhook |
| `http-request` | `url` (secret), `method` | Generic HTTP request (raw event JSON) |
| `custom` | Full `webhook:` block | Escape hatch — supply a complete webhook config |
| `email` | `api-key` (secret), `from`, `to` | Send email via SendGrid |
| `cache-invalidate` | `api-token` (secret), `zone-id`, `url-template` | Purge Cloudflare cache |
| `vector-upsert` | `api-key` (secret), `index-host`, `vector-field` | Upsert vectors to Pinecone |
| `inngest` | `event-key` (secret) | Send events to Inngest |
| `triggerdev` | `api-key` (secret) | Trigger Trigger.dev tasks |

Secret params must use `${VAR}` env-var references — they are never logged or included in the OpenAPI spec.

### Routing rules

Each action has an optional `match:` block that controls which events it receives. All three conditions are AND-ed; omitting any means "match all".

| Field | Syntax | Example |
|---|---|---|
| `tables` | Exact names or globs (`*`, `public.*`, `*.orders`) | `["public.orders", "analytics.*"]` |
| `operations` | `insert`, `update`, `delete`, `read` | `["insert", "update"]` |
| `where` | SQL-like filter with `before.` / `after.` prefixes | `"after.status = 'shipped'"` |

### OpenAPI

When a network output (SSE, gRPC, queue sinks, or `output: webhook`) or `output: none` is used, kaptanto serves a machine-readable spec at `/openapi.json` describing the configured actions, their parameter names, and routing rules.

### Integration examples

Self-contained examples for workflow platforms live in `examples/`:

- **n8n** — `examples/integrations/n8n-trigger/` (SSE trigger node, see also `packages/n8n-nodes-kaptanto/`)
- **Inngest** — `examples/integrations/inngest/` (Docker Compose + function handler)
- **Trigger.dev** — `examples/integrations/trigger-dev/` (task definition + config)

## AI-native

Kaptanto speaks agent and RAG workflows without a sidecar: an optional MCP server, a vector sink, fail-open `ai_context` enrichment, serverless action types, and typed SDKs.

### MCP quickstart

Enable the Model Context Protocol server (own listener; disabled by default — MCP-04):

```yaml
output: none
mcp:
  enabled: true
  port: 7655
  api-keys:
    - name: claude-desktop
      key: ${MCP_API_KEY}
      tables: ["public.orders"]
      redact:
        - tables: ["public.orders"]
          columns: ["customer"]
```

Point Claude Desktop (or any streamable-HTTP MCP client) at it. Replace `<MCP_API_KEY>` with the same bearer token you export as `MCP_API_KEY`:

```json
{
  "mcpServers": {
    "kaptanto": {
      "type": "http",
      "url": "http://localhost:7655",
      "headers": {
        "Authorization": "Bearer <MCP_API_KEY>"
      }
    }
  }
}
```

Tools: `list_tables`, `get_table_schema`, `subscribe_to_changes`, `get_recent_events`, `unsubscribe`, `get_event_by_id`. Every result is ACL-filtered and redacted (MCP-01); subscriptions are session-scoped (MCP-02); every call is audited (MCP-03). Full demo: `examples/ai/mcp-agent/`.

### Vector sink (local RAG)

Embed row text with a local Ollama model and upsert into pgvector — no cloud API keys:

```yaml
output: vector
tables:
  public.documents: {}
sinks:
  vector:
    source:
      columns: [title, body]
    embedder:
      provider: openai
      base-url: http://localhost:11434/v1
      model: nomic-embed-text
      dimensions: 768
      # api-key intentionally empty — local Ollama needs no auth
    store:
      provider: pgvector
      dsn: ${VECTOR_DSN}
      table: kaptanto_vectors
    metadata: [category]
    batch:
      max-events: 16
```

Unchanged text is skipped via a SHA-256 hash cache (VEC-01); embedders must return one vector per input in order (VEC-02); vector IDs are `schema.table:<canonical-key-JSON>` (VEC-03). Full stack: `examples/ai/rag-pgvector/`.

### `ai_context` enrichment

Optional HTTP enricher runs before `EventLog.Append`. Failures fail open — the unenriched event is still durable (AIC-01). Response bodies must be JSON objects ≤ 16 KiB (AIC-02):

```yaml
enrichment:
  url: http://localhost:8080/enrich
  tables: ["public.orders"]       # explicit opt-in; empty = disabled
  operations: [insert, update]    # default when omitted
  timeout: 150ms
  auth-token: ${ENRICH_TOKEN}     # optional Bearer
```

Documented `ai_context` shape (opaque to Kaptanto):

```json
{
  "intent": "refund_request",
  "entities": [{"type": "order_id", "value": "1234", "field": "id"}],
  "suggested_actions": ["notify_support"],
  "embedding": {"model": "nomic-embed-text", "vector": [0.1, 0.2]},
  "custom": {}
}
```

`ai_context` is omitted from JSON when empty and never participates in `idempotency_key`.

### Serverless actions

Invoke AWS Lambda Function URLs, Cloudflare Workers, and Vercel endpoints from CDC events via the `lambda`, `cloudflare-worker`, and `vercel` action types. See [`docs/serverless.md`](docs/serverless.md) and the landing [Serverless Actions](https://kaptanto.dev/docs/docs-serverless) page. Runnable demo: `examples/ai/lambda/`.

### SDKs

| Install | Package |
|---|---|
| `npm i @kaptanto/events` | TypeScript `ChangeEvent` types + SSE client |
| `pip install kaptanto` | Python pydantic models + httpx SSE client (`pip install 'kaptanto[langchain]'` for LangChain tools) |
| `npm i @kaptanto/mastra` | Mastra adapter — `kaptantoTrigger` + `toAgentContext` |

Demos: `examples/ai/langchain/`, `examples/ai/mastra/`.

## Security

By default, the SSE/gRPC data plane requires both a bearer token (`--auth-token` or
`KAPTANTO_AUTH_TOKEN`) and, if `--tls-cert`/`--tls-key` are set, TLS — with optional mutual TLS via
`--tls-client-ca`. `--insecure` disables all of this for local development and logs a loud warning
on startup; it is not meant for production. Outbound sink connections (Kafka, NATS, SQS, Pub/Sub,
RabbitMQ, webhook, vector) have their own independent TLS/mTLS or secret settings under each sink's
block. MCP API keys and enrichment auth tokens must use `${VAR}` references.

## Event schema

```json
{
  "id": "<ulid>",
  "idempotency_key": "<source>:<schema>.<table>:<pk>:<op>:<lsn>",
  "operation": "insert | update | delete | read | control",
  "table": "orders",
  "key": { "id": 1234 },
  "before": { "status": "pending" },
  "after":  { "status": "shipped" },
  "metadata": { "lsn": "0/1A2B3C4", "checkpoint": "...", "snapshot": false },
  "ai_context": { "intent": "refund_request" }
}
```

`read` events are emitted during the initial snapshot; `control` events mark lifecycle transitions (slot created, backfill complete). `idempotency_key` is deterministic across restarts — use it for exactly-once processing downstream. Optional `ai_context` carries opaque AI enrichment metadata when present (omitted when empty; never part of the idempotency key).

## Data directory

```
./data/
├── events/        # Badger event log
├── checkpoint.db  # Source position checkpoint (SQLite, or PostgreSQL in HA mode)
├── cursors.db     # Per-consumer cursor positions
└── backfill.db    # Snapshot progress and watermark state
```

Kaptanto is safe to restart — it resumes from the last checkpoint, and each consumer resumes from its last cursor.

## Observability

Metrics and health are served on `--port + 1` (default `:7655`):

```bash
curl http://localhost:7655/healthz   # 200 OK when healthy
curl http://localhost:7655/metrics   # Prometheus text format
```

| Metric | Type | Labels |
|---|---|---|
| `kaptanto_events_delivered_total` | Counter | `consumer`, `table`, `operation` |
| `kaptanto_consumer_lag_events` | Gauge | `consumer` |
| `kaptanto_errors_total` | Counter | `consumer`, `kind` |
| `kaptanto_source_lag_bytes` | Gauge | `source` |
| `kaptanto_checkpoint_flushes_total` | Counter | |

## Development

This directory is the Go module for the CDC binary (`go.mod` / `go.sum` at the repo root) plus its tool contracts: `.golangci.yml`, `.goreleaser.yaml`, and `.gremlins.yaml`. CI workflows live in `.github/`. Public docs are the Qwik app in `landing/`; engineering architecture is `docs/`. Publishable clients are under `packages/`. Runnable stacks are grouped in `examples/{demos,ai,integrations,supporting}/`.

```bash
make build            # CGO_ENABLED=0 static binary (default, cross-platform)
make lint             # golangci-lint over ./internal/... ./cmd/...
make test             # all tests, CGO_ENABLED=0
make test-race        # race detector (requires CGO)
make cover            # coverage run; fails below the configured threshold
make verify-no-cgo    # cross-compile linux/amd64 + darwin/arm64 to confirm no CGO leakage
make build-rust       # optional Rust-accelerated binary (requires Rust 1.77+, cargo, cbindgen)
make clean            # remove binary
```

Run a single test:

```bash
go test ./internal/router -run TestPerKeyOrdering -v
```

Slower, env-gated suites (skipped by default):

```bash
POSTGRES_TEST_DSN=... MONGO_TEST_URI=... make test-integration   # live Postgres + MongoDB
POSTGRES_TEST_DSN=... make test-e2e                              # black-box binary tests, -tags e2e
make mutation                                                    # gremlins over router/eventlog/parser/backfill
```

See `CLAUDE.md` for the full architecture/package reference and the critical invariants (durability, per-key ordering, watermark consistency, etc.) that these tests enforce.

## License

Apache 2.0
