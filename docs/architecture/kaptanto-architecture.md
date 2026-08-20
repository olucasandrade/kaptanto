# Kaptanto Architecture — Companion Document

Read alongside the diagram — either the generated Excalidraw board (produced from
`excalidraw-generation-prompt.md`) or the equivalent Mermaid pair:
- `kaptanto-architecture.mmd` — the main pipeline (sources → parser → EventLog → checkpoint →
  router → outputs) plus the parallel backfill path and the runtime data directory.
- `kaptanto-invariants.mmd` — cross-cutting components, the critical invariants, and a note on
  invariant coverage in the codebase beyond what's diagrammed.

On the Excalidraw board the layout resolved to: Critical Invariants (far left, one consolidated
box) and Cross-cutting Components (7 stacked boxes) beside the large Main Pipeline container
(Sources → Parser → EventLog → Checkpoint/Router → 8 Outputs), with Backfill and the Runtime Data
Directory panel sitting side by side below the pipeline. Render the `.mmd` files with the Mermaid
Live Editor (mermaid.live), a Markdown preview that supports Mermaid (VS Code, GitHub), or `mmdc`
(`@mermaid-js/mermaid-cli`) to export to PNG/SVG for a slide deck.
This doc explains *why* each box on the diagrams exists and how the pieces fit together.

## 0. Codebase review note

This diagram set is now kept in sync with `CLAUDE.md` (root of the repo), which was itself
revised after checking against the actual code rather than trusting prior docs. Two corrections
carried over from that pass:

1. **`internal/output/` has five broker sink packages** — `kafka`, `nats`, `sqs`, `pubsub`, and
   `rabbitmq` — alongside `stdout`, `sse`, and `grpc`. Each implements `router.Consumer` the same
   way the direct outputs do, and each is independently configurable under `Config.Sinks`
   (`internal/config/config.go`). CLAUDE.md's package table and this diagram's Outputs band both
   list all eight now.
2. **CLAUDE.md's invariant list is a curated headline set, not the full inventory.** A repo-wide
   scan (`grep -rhoE '[A-Z]{3}-[0-9]{2}' internal/`) turns up ~40 distinct invariant codes
   (`BKF-01..05`, `CFG-01..06`, `EVT-01/03/04`, `RCC-01..03`, `SRC-01..08/12`, `DLV-02..04`,
   `OUT-07/08`, `LOG-01..04`, etc.). This diagram mirrors CLAUDE.md's seven (the original six plus
   `DLV-02/03/04` for broker sinks) and includes a note pointing at the full grep — enumerating
   all ~40 would defeat the point of a presentable overview.

Also picked up from the latest CLAUDE.md revision and now reflected in the cross-cutting diagram:
**cluster/shared-cursor mode** (`--cluster` / `--cluster-dsn`, `PostgresCursorStore`, NATS
JetStream cluster peers via `--cluster-peers`) as distinct from single-node HA leader election,
and **server-side security** (`ServerTLS`/mTLS and `auth-token`/`KAPTANTO_AUTH_TOKEN` on the
SSE/gRPC servers, with `--insecure` as the explicit, loudly-warned opt-out).

Everything else in CLAUDE.md's description of the pipeline, invariants, and file layout checked
out against the code (EventLog partitioning, dedup, checkpoint ordering, router per-key retry,
SSE/gRPC method names, watermark/keyset backfill behavior).

## 1. What Kaptanto is

A single Go binary for universal database Change Data Capture (CDC). It watches Postgres (via
WAL logical replication) and MongoDB (via Change Streams) for row/document changes and streams
them out as a normalized `ChangeEvent` over stdout (NDJSON), SSE, gRPC, or one of five message
broker sinks (Kafka, NATS, SQS, Google Pub/Sub, RabbitMQ). "Kaptanto" = "who captures" in
Esperanto.

## 2. The main pipeline

1. **Source connectors** (`internal/source/postgres`, `internal/source/mongodb`) open a
   replication stream and emit raw change records. Postgres uses a logical replication slot with
   heartbeats and reconnect backoff, feeding **WAL data** to the parser. MongoDB uses Change
   Streams with resume tokens, falling back to a full snapshot on `InvalidResumeToken`, feeding a
   **change stream** to the parser.
2. **Parser** (`internal/parser/pgoutput`, `mongodb/normalizer.go`) turns source-specific wire
   formats into the unified `ChangeEvent` struct. For Postgres, a `RelationCache` maps relation
   IDs to schema, and a `TOASTCache` reconstructs unchanged large columns that Postgres omits from
   UPDATE payloads.
3. **EventLog** (`internal/eventlog/badger.go`) is the durability boundary. It's a Badger-backed
   append-only store, partitioned into 64 buckets by FNV-1a hash of the key, with TTL and
   dedup-by-`IdempotencyKey`. This is what makes the pipeline crash-safe.
4. **Checkpoint** — only once `EventLog.Append()` succeeds does the source's LSN/resume-token get
   persisted (invariant **CHK-01**). If the process crashes between receiving an event and
   checkpointing, the source replays it on reconnect, and EventLog's dedup silently absorbs the
   duplicate (second insert gets `seq=0`).
5. **Router** (`internal/router/router.go`) fans events out to registered consumers/outputs. It
   guarantees per-key ordering (**RTR-04**): if delivery for one primary key fails, only that
   key's queue blocks (see `internal/router/retry.go`) — other keys keep flowing. It updates
   per-consumer delivery position in `cursors.db` after every successful delivery.
6. **Outputs — all implement `router.Consumer`**: stdout NDJSON, an SSE server (`/events` with
   consumer/table/operation filters), a gRPC server (`Subscribe`/`Acknowledge` RPCs), and five
   message-broker sinks — Kafka, NATS, SQS, Google Pub/Sub, RabbitMQ. The Kafka sink is
   illustrative of the pattern all broker sinks follow: it uses the CDC key to pick a partition
   (**DLV-02**, per-key ordering within a topic), does a blocking `ProduceSync` so the router's
   cursor only advances after the broker acknowledges (**CHK-01** applied at the sink), never
   retries internally — that's the `RetryScheduler`'s job (**DLV-03**) — and stamps every message
   with an idempotency key/header for downstream dedup (**DLV-04**). The SSE and gRPC servers are
   additionally gated by `ServerTLS` (TLS, optional mTLS via a client CA) and a static
   `auth-token`/`KAPTANTO_AUTH_TOKEN` bearer token on the data plane; `--insecure` disables both
   with a startup warning and is not meant for production.

## 3. Backfill — the parallel path

Backfill (`internal/backfill/backfill.go`) runs concurrently with WAL/Change-Stream tailing to
seed consumers with existing data. Postgres backfill queries use a **separate connection**
(**SRC-01**) from the replication stream, since a replication-mode connection can't run regular
queries. Backfill paginates using **keyset cursors** (`cursor.go`), never `OFFSET`, because OFFSET
pagination silently skips or duplicates rows under concurrent writes.

Because backfill and live streaming run at the same time, a snapshot row can be older than a WAL
event for the same key that's already landed in EventLog. The `WatermarkChecker` discards such
stale snapshot rows — but only if it hashes keys into the *same 64 partitions* EventLog uses
(**BKF-02**). Only watermark-checked rows are appended to EventLog. Backfill progress and
watermark state are saved to `backfill.db`.

## 4. Cross-cutting components

- **HA leader election** (`internal/ha/leader.go`, **CFG-01**): uses a Postgres advisory lock so
  only one replica is active at a time; failover in ~5s. Enabled via `--ha` / `--node-id`.
- **Cluster / shared cursors**: a distinct mode from single-node HA — `--cluster` +
  `--cluster-dsn` point multiple nodes at a shared `PostgresCursorStore` so consumer cursor state
  is consistent across the cluster; `--cluster-peers` configures NATS JetStream cluster route
  addresses (with `--nats-cluster-port`, default 6222) when NATS is the sink.
- **Observability** (`internal/observability/metrics.go`): a private `prometheus.Registry` (no
  global state — important for tests), exposing `/metrics` and `/healthz`.
- **Config** (`internal/config/config.go`): YAML config merged with CLI flags; flags always win.
  Also where every broker sink (Kafka/NATS/SQS/PubSub/RabbitMQ) gets its connection config, and
  where `ServerTLS`/HA/cluster settings live.
- **Server security**: `ServerTLS` config enables TLS (and optional mutual TLS via a client CA)
  on the inbound SSE/gRPC servers — separate from each sink's own outbound TLS config. A static
  `auth-token` (or `KAPTANTO_AUTH_TOKEN` env var) bearer-token-gates the SSE/gRPC data plane;
  `--insecure` is the explicit, loudly-warned-at-startup opt-out for local development.
- **CLI/assembly** (`internal/cmd/root.go`): Cobra command that wires the whole pipeline together
  and handles graceful shutdown.
- **ChangeEvent** (`internal/event/event.go`): the single struct every source normalizes into —
  ULID-based ID, unified insert/update/delete/read/control operation types.

## 5. Critical invariants (the CLAUDE.md headline set)

| ID | Rule | Why it matters |
|----|------|-----------------|
| CHK-01 | Checkpoint advances only after `EventLog.Append()` succeeds | Guarantees at-least-once delivery; dedup handles the "at-least" part |
| RTR-04 | Per-key ordering; one stuck key doesn't block others | Correctness (no out-of-order updates) without sacrificing throughput |
| BKF-02 | WatermarkChecker and EventLog use the same 64-partition hash | Prevents stale snapshot rows from overwriting newer WAL data |
| TOAST | Parser merges omitted large columns from TOASTCache | Postgres UPDATEs omit unchanged TOASTed columns by default |
| Keyset cursors | Backfill never uses OFFSET | OFFSET breaks / skips rows under concurrent writes |
| SRC-01 | Postgres replication connection ≠ snapshot connection | Replication-mode connections can't run regular queries |
| DLV-02/03/04 | Broker sinks: key-based partition routing, no internal retry, idempotency key on every message | Same durability/ordering guarantees extended to Kafka/NATS/SQS/PubSub/RabbitMQ |

This is a curated subset for onboarding, not exhaustive — see §0 above.

## 6. Runtime data directory

```
./data/
├── events/        # Badger event log (EventLog)
├── checkpoint.db  # SQLite: source LSN (non-HA mode)
├── cursors.db     # SQLite: per-consumer, per-partition delivery positions
└── backfill.db    # SQLite: snapshot progress + watermark state
```

## 7. Where to go deeper

- `docs/architecture/technical-specification.md` — authoritative architecture reference.
- `CLAUDE.md` — quick-reference table of every key package and file, including build/lint/test
  commands (`make lint`, `make cover`, env-gated `make test-integration`/`test-e2e`/`mutation`).
- `internal/router/router.go` + `retry.go` — best place to start reading code; it ties
  EventLog, cursors, and every output (including the broker sinks) together.
- `internal/output/kafka/consumer.go` — best single file to read for how a broker sink implements
  CHK-01/DLV-02/03/04 in practice.
- `bench/` — throughput/latency comparison harness vs Debezium, Sequin, PeerDB.
