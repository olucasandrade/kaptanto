# Presenting the Kaptanto Architecture Diagram — Script

Open the generated Excalidraw board (or `kaptanto-architecture.mmd` + `kaptanto-invariants.mmd`
as the equivalent Mermaid pair). The board reads left to right, then top to bottom: **Critical
Invariants** (far left, one tall consolidated box) and **Cross-cutting Components** (7 stacked
boxes, next column) sit beside the large **Main Pipeline** container on the right, which holds
Sources → Parser → EventLog → Checkpoint/Router → 8 Outputs. Below the pipeline, bottom-left, sit
**Backfill** (its own dashed sub-lane) and the **Runtime Data Directory** panel side by side. A
small title card sits top-right. Suggested pacing: ~9–11 minutes total.

Presenting tip: since the invariants panel renders as one consolidated block of text rather than
separate numbered boxes, read it as a single unit near the end rather than pointing at individual
lines — call out CHK-01/RTR-04/BKF-02/SRC-01/DLV-02-04 by name as you speak instead.

## 1. Framing (30s)
"Kaptanto is a single Go binary for CDC — Change Data Capture. It watches a database for row or
document changes and streams them out somewhere else, in real time, without data loss. 'Kaptanto'
means 'who captures' in Esperanto."

## 2. The main pipeline (3.5 min)
Follow the large Main Pipeline container, left to right:

- **Sources**: "Two databases today: Postgres, sending WAL data to the parser via logical
  replication, and MongoDB, sending oplog events to the parser via Change Streams. Both are just
  different wire formats for the same idea — 'here's what changed.'" (Point out the dashed
  "snapshot query (SRC-01)" arrow running from Postgres down to the Backfill lane — that's a
  second, separate connection, covered in section 3.)
- **Parser**: "Whatever format the source used, the parser normalizes it into one struct —
  `ChangeEvent`. Postgres UPDATEs sometimes omit unchanged large columns, so the parser
  reconstructs them from a TOASTCache before handing the event onward."
- **EventLog**: "This is the durability boundary — the box I'd point to first if someone asks
  'what makes this safe to run in production.' Everything is appended here, backed by Badger,
  before we tell the source 'okay, I've got it.' It's partitioned 64 ways by a hash of the key,
  and it dedups by an idempotency key, so replays are harmless."
- **Checkpoint**: "We only save the checkpoint — the resume position — *after* that EventLog
  append succeeds. That ordering is invariant CHK-01. If we crash in between, the source just
  replays, and EventLog throws away the duplicate."
- **Router**: "The router fans events out to whoever's listening, and guarantees ordering per
  primary key — invariant RTR-04. If delivery to one key's consumer is stuck, that's isolated;
  every other key keeps moving. It also writes the delivery position to `cursors.db` after each
  successful send."
- **Outputs**: "This is the part I actually had to correct after checking the code against the
  original diagram — there are eight outputs, not three. Direct ones: stdout as newline-delimited
  JSON, an SSE endpoint, and gRPC streaming. And five message-broker sinks: Kafka, NATS, SQS,
  Google Pub/Sub, and RabbitMQ — all implementing the same `router.Consumer` interface. Kafka's
  the clearest example of the pattern: it picks a partition by the CDC key so ordering holds
  within a topic, blocks on `ProduceSync` so the router cursor won't advance until the broker
  acks, never retries internally — that's a separate retry scheduler's job — and stamps every
  message with an idempotency key for downstream dedup. The SSE and gRPC servers also carry their
  own security: optional TLS/mTLS and a bearer auth token gate the data plane, with an explicit
  `--insecure` flag if you want to disable that for local dev."

## 3. Backfill — the nested sub-lane (1.5 min)
"CDC alone only gives you changes *from now on*. To get existing data, backfill runs concurrently,
inside its own box because it's a parallel path, not a sequential step. It paginates with keyset
cursors — never OFFSET, because OFFSET breaks under concurrent writes. Its Postgres queries use a
completely separate connection from the replication stream — invariant SRC-01 — because a
replication-mode connection literally can't run regular queries. The tricky part: backfill and
live streaming run at the same time, so a snapshot row can be stale by the time it lands. The
WatermarkChecker throws out any snapshot row where a newer WAL event for that same key already
exists — and it can only do that correctly because it hashes keys into the exact same 64
partitions the EventLog uses. That's BKF-02. Only the rows that survive that check get appended."

## 4. Cross-cutting components (2 min)
Move to the Cross-cutting Components box: "These support the pipeline rather than sit in it. HA
leader election uses a Postgres advisory lock so only one instance is active — failover in about
five seconds; that's `--ha`/`--node-id`. There's a second, separate mode worth not confusing with
HA: cluster mode, `--cluster`/`--cluster-dsn`, which points multiple nodes at a shared cursor
store in Postgres so consumer cursor state is consistent across the whole cluster rather than
per-node — and if NATS is your sink, `--cluster-peers` wires up its own JetStream cluster
routing. Observability exposes Prometheus metrics and a health check. Config merges YAML with CLI
flags, flags always winning — and it's also where each of those five broker sinks gets its
connection settings, plus server TLS settings. Which is the last box: server security — TLS,
optional mutual TLS, and a bearer token gate the SSE/gRPC data plane by default; that's meant to
be off only when you explicitly pass `--insecure`. The CLI package is where all of this gets wired
together and shut down gracefully."

## 5. Critical invariants panel (1.5 min)
"These are the ones that must never break, because breaking any of them means silent data loss or
corruption, not a crash you'd notice." Walk through CHK-01, RTR-04, BKF-02, TOAST, keyset cursors,
SRC-01 (already covered above in context), then:
- "I added one more box here: DLV-02 through DLV-04. Those aren't in the project's own
  architecture doc yet, but they're documented directly in the Kafka sink's source comments, and
  they extend the same guarantees — durability, ordering, idempotency — out to the broker sinks."
- "One honest caveat: when I grepped the codebase for invariant IDs, there are around forty
  distinct codes, not six. What's on this panel is the curated 'need to know on day one' subset."

## 6. Runtime data directory (30s)
Point at the panel sitting next to Backfill, bottom-left: "Everything durable lives under
`./data/` — the event log, and three SQLite databases for checkpoint, per-consumer cursors, and
backfill progress. It's drawn right beside Backfill because that's where most of its writes come
from, but the Checkpoint and cursors.db boxes up in the main pipeline write here too."

## 7. Close (30s)
"The one file to point people at for more depth is `docs/architecture/technical-specification.md`. `CLAUDE.md` has the package-by-package quick reference — it's now up to date with all
eight outputs, HA vs. cluster mode, and server security, plus the actual `make lint`/`make cover`/
env-gated integration and mutation test commands. If you want to read code first, start at
`internal/router/router.go` to see how everything ties together, then `internal/output/kafka/
consumer.go` for the clearest example of a sink implementing the durability/ordering contract."

## Anticipated questions
- **"Why does the diagram show eight outputs when the docs only mentioned three?"** → That was a
  gap in an earlier pass of both the diagram and CLAUDE.md — neither had been checked against
  `internal/output/` directly. Both have since been corrected against the actual code.
- **"What's the difference between HA and cluster mode?"** → HA (`--ha`) is single-active-writer
  leader election via a Postgres advisory lock — only one node reads the source at a time. Cluster
  mode (`--cluster`) is about consumer cursor state being shared across nodes via
  `PostgresCursorStore`, independent of which node is the active source reader.
- **"What happens on exactly-once delivery?"** → We guarantee at-least-once from source to
  EventLog (dedup on IdempotencyKey), and per-key ordering downstream — not global exactly-once
  across the whole pipeline. Broker sinks add their own idempotency key/header so the consuming
  system can dedup too.
- **"Why 64 partitions specifically?"** → Arbitrary fixed constant shared between EventLog and
  WatermarkChecker; the number itself doesn't matter, consistency between the two does (BKF-02).
- **"Why not OFFSET for backfill pagination?"** → Rows can shift between pages under concurrent
  writes, causing skips or duplicates; keyset (WHERE pk > last_seen) is stable regardless of
  concurrent writes.
