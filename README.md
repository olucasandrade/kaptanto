# Kaptanto

Every insert, update, and delete from your Postgres or MongoDB database, streamed the moment it happens.

Kaptanto is a CDC tool in one static Go binary.

```bash
./kaptanto \
  --source "postgres://localhost:5432/mydb" \
  --tables public.orders,public.payments \
  --output stdout

{"op":"insert","table":"orders","after":{"id":1234,"status":"pending","total":99.99}}
{"op":"update","table":"orders","before":{"status":"pending"},"after":{"status":"shipped"}}
```

## What it does

kaptanto connects to the Postgres WAL (logical replication) or MongoDB Change Streams and emits a unified stream of events. Every row change is captured, durably logged, and delivered to your consumers — even across crashes and restarts.

It handles the hard parts automatically: initial snapshots, watermark-coordinated backfills, per-key ordering, consumer cursor tracking, and high-availability failover.

## Features

- **Zero runtime dependencies** — static binary, no sidecars, no agents, no brokers
- **Durable event log** — every event is written to the embedded log before the source checkpoint advances; a crash never loses an event
- **Multiple outputs** — stdout (NDJSON), HTTP Server-Sent Events, gRPC streaming
- **Consistent backfills** — snapshot and live stream run concurrently; watermark coordination prevents duplicate or stale rows
- **Per-key ordering** — events for the same primary key always arrive in commit order
- **Per-consumer cursors** — each consumer tracks its own position; reconnect at any time and resume exactly where you left off
- **Filtering** — table, column, operation, and SQL WHERE condition filters
- **High availability** — leader election via Postgres advisory lock; standby takes over in ~5 seconds if the primary crashes
- **Observability** — Prometheus metrics at `/metrics`, health check at `/healthz`

## Quick Start

### 1. Build

```bash
make build
# Produces: ./kaptanto
```

### 2. Enable logical replication

In `postgresql.conf`:

```
wal_level = logical
```

Grant replication access:

```sql
CREATE ROLE kaptanto WITH REPLICATION LOGIN PASSWORD 'secret';
GRANT SELECT ON TABLE public.orders, public.payments TO kaptanto;
```

For full before/after values on updates and deletes:

```sql
ALTER TABLE public.orders REPLICA IDENTITY FULL;
ALTER TABLE public.payments REPLICA IDENTITY FULL;
```

### 3. Run

```bash
./kaptanto \
  --source "postgres://kaptanto:secret@localhost:5432/mydb" \
  --tables public.orders,public.payments \
  --output stdout
```

Events arrive as NDJSON on stdout. Pipe to `jq`, a processor, or any program that reads stdin.

## Outputs

### stdout

One JSON line per event. Ideal for Unix pipes and container log collectors.

```bash
./kaptanto --source "..." --tables public.orders --output stdout | jq .
```

### SSE (Server-Sent Events)

Starts an HTTP server at `/events`. Each connected client is an independent consumer with its own cursor.

```bash
./kaptanto --source "..." --tables public.orders --output sse --port 7654
curl -N http://localhost:7654/events?consumer=worker-1
```

Clients that disconnect and reconnect resume from where they left off — no events missed.

| Parameter     | Description                                     | Example                      |
|---------------|-------------------------------------------------|------------------------------|
| `consumer`    | Stable consumer ID for cursor resumption        | `?consumer=worker-1`         |
| `tables`      | Comma-separated table allow-list (empty = all)  | `?tables=orders,users`       |
| `operations`  | Comma-separated operation filter (empty = all)  | `?operations=insert,update`  |

### gRPC

High-throughput typed streaming via Protocol Buffers.

```bash
./kaptanto --source "..." --tables public.orders --output grpc --port 7654
```

```protobuf
service CDCService {
  rpc Subscribe (SubscribeRequest) returns (stream ChangeEvent);
  rpc Acknowledge (AckRequest) returns (AckResponse);
}
```

`Subscribe` opens a stream. `Acknowledge` advances your cursor after processing a batch.

## Configuration

All flags can be set via CLI or a YAML config file. CLI flags take precedence.

```bash
./kaptanto --config kaptanto.yaml
```

| Flag           | Default    | Description                                             |
|----------------|------------|---------------------------------------------------------|
| `--source`     | (required) | Database connection string                              |
| `--tables`     | (required) | Tables to replicate, e.g. `public.orders public.users`  |
| `--output`     | `stdout`   | `stdout`, `sse`, or `grpc`                              |
| `--port`       | `7654`     | TCP port for SSE or gRPC server                         |
| `--data-dir`   | `./data`   | Directory for event log and checkpoints                 |
| `--retention`  | `1h`       | Event log TTL (e.g. `24h`, `7d`)                        |
| `--log-level`  | `info`     | `debug`, `info`, `warn`, `error`                        |
| `--config`     |            | Path to YAML config file                                |

### YAML example

```yaml
source: "postgres://kaptanto:secret@localhost:5432/mydb"
output: sse
port: 7654
data_dir: /var/lib/kaptanto
retention: 24h

tables:
  - name: public.orders
    columns: [id, status, total]
    where: "status != 'archived'"

  - name: public.users
    columns: [id, email, created_at]
```

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
  "metadata": {
    "lsn": "0/1A2B3C4",
    "checkpoint": "...",
    "snapshot": false
  }
}
```

- `read` — emitted during the initial snapshot
- `control` — emitted for lifecycle events (slot created, backfill complete, etc.)
- `before` is `null` for inserts; `after` is `null` for deletes
- `idempotency_key` is deterministic and stable across restarts — use it for exactly-once processing on the consumer side

## Data directory

```
./data/
├── events/        # Badger event log
├── checkpoint.db  # Source position checkpoint (SQLite)
├── cursors.db     # Per-consumer cursor positions (SQLite)
└── backfill.db    # Snapshot progress and watermark state (SQLite)
```

kaptanto is safe to restart. It resumes from the last checkpoint, and each consumer resumes from its last cursor.

## Observability

When running `sse` or `grpc` mode, metrics and health are available at `--port + 1` (default `:7655`):

```bash
curl http://localhost:7655/healthz   # 200 OK when healthy
curl http://localhost:7655/metrics   # Prometheus text format
```

| Metric                              | Type    | Labels                            |
|-------------------------------------|---------|-----------------------------------|
| `kaptanto_events_delivered_total`   | Counter | `consumer`, `table`, `operation`  |
| `kaptanto_consumer_lag_events`      | Gauge   | `consumer`                        |
| `kaptanto_errors_total`             | Counter | `consumer`, `kind`                |
| `kaptanto_source_lag_bytes`         | Gauge   | `source`                          |
| `kaptanto_checkpoint_flushes_total` | Counter |                                   |

## Development

```bash
make build        # Compile binary
make test         # Run all tests
make test-race    # Run tests with race detector
make clean        # Remove binary
```

## License

Apache 2.0
