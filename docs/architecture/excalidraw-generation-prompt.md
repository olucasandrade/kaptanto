# Prompt: README architecture diagram (`kaptanto_architecture.png`) in Excalidraw

Paste only the block under `## PROMPT` into Excalidraw AI (limit 10,000 characters).
Export PNG 2x and replace `docs/architecture/kaptanto_architecture.png`.

---

## PROMPT

Landscape Excalidraw, sketchy roughness 1, rounded boxes, ~2000x1300. Draw Kaptanto's architecture so a reader can follow how a database change becomes a delivered event. Same layout as a typical README system diagram: data directory top-left, sources top-right, backfill on the left, Parser → Enrich → EventLog across the center, Checkpoint to the right, Router below EventLog, outputs along the bottom, Actions and MCP to the right of Router.

Do not label anything with a version, release, issue, ticket, or invariant code. No "new", "1.0", gold pills, or IDs like CHK-01. No Redis sink. Per-key ordering, not per-tenant.

Title: **Kaptanto**. Subtitle: one static Go binary — capture database changes and deliver them in order.

Arrows: solid black = data (WAL data, oplog events, ChangeEvent, events); dashed purple = control (snapshot query, append succeeded, update position, dead-letter). Every arrow labeled.

Fills (darker matching stroke): sources #a5d8ff; parser #b2f2bb; enrich #c5f6fa; backfill #ffd8a8; EventLog #ffc9c9 (~20% larger); checkpoint / dead-letter / data-dir #eebefa; router #ffd8a8 (~20% larger); outputs / Actions / MCP #d0bfff. Each box: bold title + 1–2 short detail lines.

**Data directory (top-left):** ./data/
events/ durable log · checkpoint.db source position · cursors.db per-consumer position · backfill.db snapshot progress · dlq.db undeliverable events
Dashed lines toward EventLog, Checkpoint, cursors, Backfill, dead-letter store.

**Sources (top-right, blue):**
- Postgres — tails the WAL (logical replication). Slot, heartbeats, reconnect.
- MongoDB — tails Change Streams. Resume token; fresh snapshot if the token is invalid.
Arrows into Parser: WAL data / oplog events. Dashed into Backfill: snapshot query on a separate connection (replication connections cannot run normal SQL).

**Backfill (left, dashed container):** runs at the same time as live tailing
- Snapshot of existing rows, paginated by keyset (never OFFSET)
- Watermark: skip a snapshot row if a newer live change for that key is already in the log
- backfill.db cylinder
Arrow: surviving snapshot rows → EventLog.

**Center:**
Parser (green): turns WAL / oplog into one ChangeEvent. Rebuilds omitted large Postgres columns from a TOAST cache.
Enrich (cyan, between Parser and EventLog): optional HTTP call that may attach ai_context. If it fails, the event is still saved without it. Skip entirely when no URL/tables are set.
EventLog (red, large): durable append-only store, 64 partitions, TTL, dedup by idempotency key. This is the crash-safety boundary.
Down: events → Router. Dashed right: after a successful append → Checkpoint.

**Right of EventLog:**
Checkpoint: save the source resume position only after the event is in the log. SQLite locally, PostgreSQL when running HA.
cursors.db: each consumer's delivery position, updated after a successful send.
Dead-letter store: after retries are exhausted, park the event and move on so one bad key does not block forever. Dashed from Router: dead-letter.

**Router (orange, large):** fan-out to every consumer. Events for the same primary key stay in commit order; a failure blocks only that key. Broker sinks do not retry on their own — the router does. Fan-out down to outputs, right to MCP, down-right to Actions.

**Outputs (violet) — every output is a consumer of the router**
Row 1: stdout (NDJSON) · SSE /events (filter by consumer, table, operation; TLS and bearer token) · gRPC Subscribe + Acknowledge · Kafka (partition by key, wait for ack, stamp an idempotency header) · NATS · SQS (FIFO) · Google Pub/Sub · RabbitMQ
Row 2: webhook (HTTP POST, optional HMAC / SigV4, go-template or jq) · vector (extract text → embed → upsert/delete in pgvector, Pinecone, or Qdrant; skip unchanged text)
Also served on the HTTP port: /metrics, /healthz, /openapi.json (gRPC puts those on port+1). stdout has no HTTP.

**Right of Router:**
Actions: YAML rules that turn matching events into Slack, Discord, email, cache purge, vector upsert, Inngest, Trigger.dev, HTTP, Lambda, Cloudflare Worker, or Vercel calls. Each action is just config — delivery always goes through the webhook path. Unmatched events are acknowledged. output: none runs Actions (and/or MCP) with no primary sink.
MCP: optional agent server on its own port (default 7655). Tools to list tables, get schema, subscribe, drain recent events, look up by id. Results are ACL-filtered and redacted. Subscriptions die with the session.

**Legend (tiny, bottom-left):** solid = data · dashed = control. EventLog and Router are the visual center. Two output rows if needed. No overlapping labels. No flags, CI, or package names on the drawing — product names of outputs are fine.
