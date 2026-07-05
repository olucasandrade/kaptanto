export type DocItem = { title: string; sub: string; body: string };

// docs content
export const DOCS_CONTENT: Record<string, DocItem> = {
'docs-intro': {title:'What is Kaptanto?',sub:'The fastest way to stream changes from Postgres and MongoDB to your application. One binary, no infrastructure.',body:`
<div class="dcards">
<div class="dcard" onclick="go('docs-quickstart')"><h4>Quick Start</h4><p>Go from zero to streaming in 2 minutes.</p></div>
<div class="dcard" onclick="go('docs-postgres')"><h4>Connect Postgres</h4><p>Set up WAL logical replication.</p></div>
<div class="dcard" onclick="go('docs-mongo')"><h4>Connect MongoDB</h4><p>Configure Change Streams for capture.</p></div>
<div class="dcard" onclick="go('docs-schema')"><h4>Event Schema</h4><p>Unified event format across all sources.</p></div>
</div>
<h2 class="dh2">How it works</h2>
<p class="dp">Kaptanto connects to your database's native change log — the WAL for Postgres, the oplog for MongoDB — and emits a unified stream of events. Every insert, update, and delete is captured and delivered via stdout, SSE, or gRPC.</p>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://localhost:5432/mydb \\
    --tables orders,payments --output stdout

<span class="tc"># Events stream as newline-delimited JSON (simplified — see Event Schema)</span>
{"operation":"insert","table":"orders","after":{"id":1,"status":"pending"}}
{"operation":"update","table":"orders","before":{"status":"pending"},"after":{"status":"paid"}}</div>
<p class="dp">Each real line carries the full event — <code>operation</code>, <code>id</code>, <code>idempotency_key</code>, <code>timestamp</code>, <code>key</code>, <code>before</code>, <code>after</code>, and <code>metadata</code>. See the <a onclick="go('docs-schema')">Event Schema</a> for the complete shape.</p>
<p class="dp">It runs as a single binary with zero external dependencies. No Kafka, no ZooKeeper, no JVM. Handles backfills, checkpointing, per-key ordering, and database failover natively.</p>
<div class="dcall"><p><strong>Critical invariant:</strong> The source checkpoint is never advanced until the event is durably written to the internal Event Log. If kaptanto crashes, the source re-sends from the last acknowledged position. Zero events lost.</p></div>
<h2 class="dh2">Key features</h2>
<ul class="dul">
<li><strong>Consistent backfills</strong> — Watermark-coordinated snapshots that merge seamlessly with the live WAL stream. Crash-resumable keyset cursors.</li>
<li><strong>Per-key ordering</strong> — Events for the same primary key always arrive in commit order, hashed across 64 partitions for parallel throughput.</li>
<li><strong>Idempotency keys</strong> — Every event has a deterministic, stable key for exactly-once processing.</li>
<li><strong>Poison pill isolation</strong> — Failed events block only their message group, not the pipeline. Exponential backoff (1s → 5s → 30s → 2min → 10min plateau); after 15 retries the event is logged at Error level and dropped — no persistent dead-letter store.</li>
<li><strong>High availability</strong> — Leader election via Postgres advisory locks. Automatic primary detection and failover.</li>
<li><strong>Cluster mode</strong> — Active-active delivery across nodes with embedded NATS JetStream and shared partition ownership.</li>
<li><strong>Queue sinks</strong> — Push CDC events to NATS JetStream, AWS SQS, Apache Kafka, Google Cloud Pub/Sub, or RabbitMQ with per-table routing and TLS/mTLS support.</li>
<li><strong>Named sources</strong> — One database per process today; <code>--source-id</code> namespaces the replication slot and publication so you can run several kaptanto instances against the same or different databases without collisions.</li>
<li><strong>Filtering</strong> — Table, operation, column, and SQL WHERE condition filters.</li>
</ul>`},

'docs-quickstart': {title:'Quick Start',sub:'Install kaptanto and stream your first events in under 2 minutes.',body:`
<h2 class="dh2">1. Install</h2>
<div class="dcode"><span class="tg">$</span> curl -fsSL https://get.kaptan.to | sh</div>
<p class="dp">Or with Docker:</p>
<div class="dcode"><span class="tg">$</span> docker pull olucasandrade/kaptanto:latest</div>

<h2 class="dh2">2. Configure Postgres</h2>
<p class="dp">Ensure your Postgres has logical replication enabled:</p>
<div class="dcode">-- postgresql.conf (or via ALTER SYSTEM)
wal_level = logical
max_replication_slots = 4
max_wal_senders = 4</div>
<p class="dp">Restart Postgres after changing <code>wal_level</code>.</p>

<h2 class="dh2">3. Start capturing</h2>
<div class="dcode"><span class="tg">$</span> kaptanto \\
    --source postgres://user:pass@localhost:5432/mydb \\
    --tables orders,payments \\
    --output stdout</div>
<p class="dp">Kaptanto will automatically create a replication slot and publication, snapshot existing rows, then stream real-time changes as NDJSON.</p>

<h2 class="dh2">4. Use SSE or gRPC</h2>
<p class="dp">For multi-consumer setups, use SSE or gRPC instead of stdout:</p>
<div class="dcode"><span class="tc"># SSE server</span>
<span class="tg">$</span> kaptanto --source postgres://... --output sse --port 7654 --insecure

<span class="tc"># gRPC server</span>
<span class="tg">$</span> kaptanto --source postgres://... --output grpc --port 50051 --insecure</div>
<p class="dp">Each connected client gets an independent consumer with its own cursor and checkpoint.</p>
<div class="dcall"><p><strong>Auth is required by default:</strong> SSE, gRPC, and every queue sink refuse to start without <code>--auth-token</code> (or <code>KAPTANTO_AUTH_TOKEN</code>) and TLS certificates (SSE/gRPC only). <code>--insecure</code> disables both checks for local development — never use it in production. See <a onclick="go('docs-config')">Configuration</a> for the production setup.</p></div>`},

'docs-install': {title:'Installation',sub:'Install kaptanto on Linux, macOS, or Windows.',body:`
<h2 class="dh2">Binary (recommended)</h2>
<div class="dcode"><span class="tg">$</span> curl -fsSL https://get.kaptan.to | sh</div>
<p class="dp">Downloads a statically-linked binary for your platform. No runtime dependencies.</p>

<h2 class="dh2">Docker</h2>
<div class="dcode"><span class="tg">$</span> docker pull olucasandrade/kaptanto:latest
<span class="tg">$</span> docker run olucasandrade/kaptanto --source postgres://host.docker.internal:5432/mydb --output stdout</div>

<h2 class="dh2">From source</h2>
<p class="dp">Requires Go 1.25+:</p>
<div class="dcode"><span class="tg">$</span> git clone https://github.com/olucasandrade/kaptanto
<span class="tg">$</span> cd kaptanto && go build -o kaptanto ./cmd/kaptanto</div>

<h2 class="dh2">With Rust acceleration</h2>
<p class="dp">For maximum parsing performance, compile with the Rust FFI parser:</p>
<div class="dcode"><span class="tg">$</span> make build-rust</div>
<p class="dp">Requires Rust 1.77+ and CGO. Provides 30-40% lower CPU usage for the pgoutput decoding path.</p>`},

'docs-postgres': {title:'Connect Postgres',sub:'Configure Postgres for CDC with kaptanto.',body:`
<h2 class="dh2">Requirements</h2>
<ul class="dul">
<li><strong>Postgres 14–17</strong> — The tested and supported range for the <code>pgoutput</code> logical decoding plugin (included in Postgres core since v10).</li>
<li><strong>wal_level = logical</strong> — Enables the write-ahead log to include logical change data.</li>
<li><strong>max_replication_slots >= 1</strong> — At least one slot for kaptanto.</li>
<li><strong>max_wal_senders >= 1</strong> — Allows kaptanto to connect as a replication client.</li>
</ul>

<h2 class="dh2">Recommended: REPLICA IDENTITY FULL</h2>
<p class="dp">For complete before/after values on updates and deletes:</p>
<div class="dcode">ALTER TABLE orders REPLICA IDENTITY FULL;
ALTER TABLE payments REPLICA IDENTITY FULL;</div>
<p class="dp">Without this, updates only include the primary key in the <code>before</code> field. Kaptanto warns on connect if tables use the default identity.</p>

<h2 class="dh2">Cloud-hosted databases</h2>
<table class="dtbl"><thead><tr><th>Provider</th><th>wal_level</th><th>Notes</th></tr></thead><tbody>
<tr><td>AWS RDS</td><td>Set via parameter group</td><td>rds.logical_replication = 1</td></tr>
<tr><td>GCP Cloud SQL</td><td>Set via flags</td><td>cloudsql.logical_decoding = on</td></tr>
<tr><td>Supabase</td><td>Enabled by default</td><td>Works out of the box</td></tr>
<tr><td>Neon</td><td>Enabled by default</td><td>Works out of the box</td></tr>
</tbody></table>

<h2 class="dh2">Failover handling</h2>
<p class="dp">Kaptanto supports multi-host DSNs for automatic primary detection:</p>
<div class="dcode">postgres://user:pass@primary:5432,standby:5432/mydb?target_session_attrs=read-write</div>
<p class="dp">On failover, kaptanto reconnects, verifies it is connected to the primary (not a standby), checks for the replication slot, and resumes from the last checkpoint.</p>`},

'docs-mongo': {title:'Connect MongoDB',sub:'Configure MongoDB Change Streams for CDC with kaptanto.',body:`
<h2 class="dh2">Requirements</h2>
<ul class="dul">
<li><strong>MongoDB 4.0+</strong> — Change Streams require a replica set or sharded cluster.</li>
<li><strong>Replica set</strong> — Change Streams do not work on standalone instances.</li>
</ul>

<h2 class="dh2">Connection string</h2>
<div class="dcode"><span class="tg">$</span> kaptanto \\
    --source mongodb://user:pass@rs1:27017,rs2:27017/analytics?replicaSet=rs0 \\
    --tables page_views,user_sessions \\
    --output stdout</div>
<p class="dp">For MongoDB, <code>--tables</code> names the collections to capture.</p>

<h2 class="dh2">Failover</h2>
<p class="dp">Handled natively by the MongoDB driver. Replica set elections trigger automatic reconnection. Resume tokens survive elections, so kaptanto resumes exactly where it left off.</p>
<p class="dp">If the oplog is truncated during a long outage and the resume token is invalid, kaptanto detects this and triggers an automatic re-snapshot.</p>

<h2 class="dh2">Schema normalization</h2>
<p class="dp">MongoDB documents are schemaless. Kaptanto emits the full document as-is in the <code>after</code> field, preserving nested structures. The <code>key</code> field contains the <code>_id</code> value.</p>`},

'docs-schema': {title:'Event Schema',sub:'Every event follows the same JSON structure regardless of source.',body:`
<h2 class="dh2">Standard event</h2>
<div class="dcode">{
  "id": "01HX7K9M3N4P5Q6R7S8T9U0V",
  "idempotency_key": "main-pg:public.orders:1234:update:0/1A2B3C4",
  "timestamp": "2026-03-06T14:32:01.847Z",
  "source": "main-pg",
  "operation": "update",
  "schema": "public",
  "table": "orders",
  "key": { "id": 1234 },
  "before": { "id": 1234, "status": "pending", "amount": 149.90 },
  "after":  { "id": 1234, "status": "settled", "amount": 149.90 },
  "metadata": {
    "lsn": "0/1A2B3C4",
    "snapshot": false
  }
}</div>
<p class="dp">Postgres sources populate <code>schema</code>; MongoDB sources populate <code>database</code> instead (the database name), since MongoDB has no schema concept. Both fields are omitted when empty.</p>

<h2 class="dh2">Operations</h2>
<table class="dtbl"><thead><tr><th>Operation</th><th>Source</th><th>Description</th></tr></thead><tbody>
<tr><td><code>insert</code></td><td>WAL / oplog</td><td>New row created</td></tr>
<tr><td><code>update</code></td><td>WAL / oplog</td><td>Existing row modified</td></tr>
<tr><td><code>delete</code></td><td>WAL / oplog</td><td>Row removed</td></tr>
<tr><td><code>read</code></td><td>Backfill</td><td>Snapshot of existing row</td></tr>
<tr><td><code>control</code></td><td>System</td><td>Pipeline state change</td></tr>
</tbody></table>

<h2 class="dh2">Idempotency key</h2>
<p class="dp">Every event has a deterministic <code>idempotency_key</code> composed of source_id, table, primary_key, operation, and position. This key is stable across restarts. Consumers use it for deduplication.</p>
<div class="dcall"><p><strong>Format:</strong> <code>{source}:{schema}.{table}:{pk}:{op}:{position}</code></p></div>`},

'docs-backfills': {title:'Backfills',sub:'How kaptanto snapshots existing data and coordinates with real-time streaming.',body:`
<h2 class="dh2">Strategies</h2>
<p class="dp">Kaptanto currently runs the <code>snapshot_and_stream</code> strategy for every table — it is the default and is not yet selectable per table via a flag or YAML field. The remaining strategies exist in the backfill engine and are documented here for completeness; per-table strategy selection is on the roadmap.</p>
<table class="dtbl"><thead><tr><th>Strategy</th><th>Behavior</th></tr></thead><tbody>
<tr><td><code>snapshot_and_stream</code></td><td>Snapshot existing rows, then stream changes. <strong>Default — the only strategy currently used.</strong></td></tr>
<tr><td><code>stream_only</code></td><td>Skip the snapshot. Only stream new changes.</td></tr>
<tr><td><code>snapshot_only</code></td><td>Snapshot then stop. One-time export.</td></tr>
<tr><td><code>snapshot_deferred</code></td><td>Record snapshot intent now; run the snapshot on the next restart.</td></tr>
<tr><td><code>snapshot_partial</code></td><td>Resume an in-progress snapshot from the last saved cursor if one exists.</td></tr>
</tbody></table>

<h2 class="dh2">Watermark coordination</h2>
<p class="dp">While snapshotting, the WAL stream continues. If a row is read during the snapshot AND updated in the WAL, kaptanto drops the stale read and relies on the WAL event. This prevents duplicate or out-of-order delivery.</p>
<p class="dp">The mechanism: for each snapshot row, kaptanto checks if the Event Log already contains a WAL event for the same primary key with a higher LSN. If so, the read is discarded.</p>

<h2 class="dh2">Keyset cursors</h2>
<p class="dp">Kaptanto uses keyset cursors (not OFFSET) for pagination. This ensures no rows are skipped even if data is inserted or deleted during the backfill.</p>
<div class="dcode">SELECT * FROM orders WHERE id > $1 ORDER BY id ASC LIMIT 5000;</div>

<h2 class="dh2">Crash recovery</h2>
<p class="dp">Backfill cursor position is persisted on every batch. If kaptanto crashes mid-backfill, it resumes from the last saved cursor, not from the beginning.</p>`},

'docs-consistency': {title:'Consistency Model',sub:'How kaptanto ensures loss-free, strictly ordered change data capture.',body:`
<h2 class="dh2">Guarantees</h2>
<table class="dtbl"><thead><tr><th>Guarantee</th><th>Description</th></tr></thead><tbody>
<tr><td>Consistency</td><td>Every row-level event is delivered at least once</td></tr>
<tr><td>Ordering</td><td>Events arrive in commit order within a message group</td></tr>
<tr><td>Throughput</td><td>Parallelized delivery across partitions</td></tr>
<tr><td>Resilience</td><td>Poison pill isolation — failed events don't halt the pipeline</td></tr>
</tbody></table>

<h2 class="dh2">At-least-once with idempotency</h2>
<p class="dp">True exactly-once delivery across a network boundary is impossible. Kaptanto provides at-least-once delivery with deterministic idempotency keys, enabling exactly-once processing on the consumer side.</p>

<h2 class="dh2">Event Log durability</h2>
<p class="dp">Every event is durably written to the embedded Event Log (Badger) before the source checkpoint is advanced. If kaptanto crashes between receiving a WAL message and writing it, the source re-sends on reconnection. The Event Log deduplicates by the deterministic <code>idempotency_key</code>, so replayed messages collapse to a single event.</p>

<h2 class="dh2">Poison pill handling</h2>
<p class="dp">Failed events are retried with exponential backoff (1s → 5s → 30s → 2min → 10min plateau). After 15 failed attempts, the event is logged at Error level and dropped — there is no persistent dead-letter store to replay from, so alert on <code>kaptanto_errors_total</code> to catch this. A failed event blocks only its own message group, not other groups in the same partition.</p>`},

'docs-ordering': {title:'Ordering and Partitions',sub:'How kaptanto maintains per-key order while maximizing throughput.',body:`
<h2 class="dh2">Message groups</h2>
<p class="dp">Events are hashed into 64 partitions by their primary key. All events for the same key land in the same partition and are delivered sequentially in commit order.</p>
<div class="dcall"><p><strong>Note:</strong> The grouping key is currently the table's primary key (<code>id</code>) and is not yet configurable per table. Custom grouping keys are on the roadmap.</p></div>

<h2 class="dh2">Partition isolation</h2>
<p class="dp">Each partition is served by a dedicated goroutine. If consumer A is slow on partition 7, partitions 0-6 and 8-63 continue at full speed for all consumers.</p>`},

'docs-stdout': {title:'stdout Output',sub:'The simplest output mode — pipe CDC events anywhere.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output stdout</div>
<p class="dp">One JSON line per event written to stdout. Pipe to jq, a subprocess, or any program that reads stdin.</p>

<h2 class="dh2">Filtering with jq</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output stdout | jq 'select(.operation == "insert")'</div>

<h2 class="dh2">Limitations</h2>
<p class="dp">Single consumer only. No acknowledgment — events are fire-and-forget. The consumer is responsible for its own checkpointing using <code>metadata.lsn</code> (Postgres) or the <code>idempotency_key</code>, which is stable across restarts.</p>`},

'docs-sse': {title:'SSE Output',sub:'Server-Sent Events for multi-consumer HTTP streaming.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output sse --port 7654 \\
    --tls-cert server.pem --tls-key server.key --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcall"><p><strong>Required:</strong> SSE refuses to start without TLS certificates and an auth token unless you pass <code>--insecure</code> (development only — see <a onclick="go('docs-config')">Configuration</a>).</p></div>

<h2 class="dh2">Connecting</h2>
<div class="dcode">GET https://localhost:7654/events?tables=orders,payments&consumer=my-service
Authorization: Bearer &lt;token&gt;</div>
<p class="dp">Each HTTP connection is an independent consumer with its own cursor. Supports <code>Last-Event-ID</code> header for automatic resume on reconnect.</p>
<div class="dcall"><p><strong>Shared-principal model:</strong> the bearer token grants access to the whole data plane, and consumer IDs are chosen by the client. Any client holding the token can attach with another service's <code>consumer</code> value and advance that cursor, causing the original consumer to skip events. Treat every token holder as fully trusted, use distinct consumer IDs per service by convention, and monitor <code>kaptanto_consumer_lag_events</code> for unexpected cursor jumps.</p></div>

<h2 class="dh2">Event format</h2>
<div class="dcode">id: 01HX7K9M3N4P5Q6R7S8T9U0V
data: {"operation":"update","table":"orders","after":{"id":1234,"status":"settled"}}

id: 01HX7K9M3N4P5Q6R7S8T9U0W
data: {"operation":"insert","table":"payments","after":{"id":5678}}</div>
<p class="dp">The <code>data:</code> payload is the full event JSON (simplified above for readability) — see the <a onclick="go('docs-schema')">Event Schema</a>.</p>`},

'docs-grpc': {title:'gRPC Output',sub:'High-performance streaming with Protocol Buffers.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output grpc --port 50051 \\
    --tls-cert server.pem --tls-key server.key --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcall"><p><strong>Required:</strong> gRPC refuses to start without TLS certificates and an auth token unless you pass <code>--insecure</code> (development only — see <a onclick="go('docs-config')">Configuration</a>).</p></div>

<h2 class="dh2">Proto definition</h2>
<div class="dcode">service CdcStream {
  rpc Subscribe(SubscribeRequest) returns (stream ChangeEvent);
  rpc Acknowledge(AcknowledgeRequest) returns (AcknowledgeResponse);
}</div>

<h2 class="dh2">Consumer pattern</h2>
<p class="dp">Call <code>Subscribe</code> to open a streaming connection. Call <code>Acknowledge</code> periodically with the checkpoint from the last processed event to advance your cursor.</p>
<p class="dp">HTTP/2 backpressure is native. If the consumer stops reading, the gRPC server detects window exhaustion and applies per-consumer backpressure.</p>`},

'docs-config': {title:'Configuration',sub:'CLI flags and YAML configuration reference.',body:`
<h2 class="dh2">CLI flags</h2>
<table class="dtbl"><thead><tr><th>Flag</th><th>Default</th><th>Description</th></tr></thead><tbody>
<tr><td><code>--source</code></td><td>required</td><td>Database connection string</td></tr>
<tr><td><code>--tables</code></td><td>required</td><td>Table names — comma-separated (<code>--tables orders,payments</code>) or repeated (<code>--tables orders --tables payments</code>)</td></tr>
<tr><td><code>--output</code></td><td>stdout</td><td>Output mode: stdout, sse, grpc, nats, sqs, kafka, pubsub, rabbitmq</td></tr>
<tr><td><code>--port</code></td><td>7654</td><td>Port for SSE/gRPC server</td></tr>
<tr><td><code>--cors-origin</code></td><td>-</td><td>SSE <code>Access-Control-Allow-Origin</code> value; empty (default) sends no CORS header, blocking cross-origin browser access</td></tr>
<tr><td><code>--config</code></td><td>-</td><td>Path to YAML config file</td></tr>
<tr><td><code>--data-dir</code></td><td>./data</td><td>Directory for Event Log and checkpoints</td></tr>
<tr><td><code>--retention</code></td><td>1h</td><td>Event Log retention period</td></tr>
<tr><td><code>--log-level</code></td><td>info</td><td>Log verbosity: debug, info, warn, error</td></tr>
<tr><td><code>--ha</code></td><td>false</td><td>Enable Postgres advisory lock leader election</td></tr>
<tr><td><code>--node-id</code></td><td>auto</td><td>Unique node identifier for HA and cluster modes</td></tr>
<tr><td><code>--source-id</code></td><td>default</td><td>Logical source identifier; determines the replication slot name (<code>kaptanto_&lt;id&gt;</code>) and publication name (<code>kaptanto_pub_&lt;id&gt;</code>) — set a distinct value per instance when running multiple kaptanto processes against the same database</td></tr>
<tr><td><code>--cluster</code></td><td>false</td><td>Enable active-active cluster mode (embedded NATS JetStream)</td></tr>
<tr><td><code>--cluster-dsn</code></td><td>-</td><td>Postgres DSN for shared cursor and backfill state in cluster mode</td></tr>
<tr><td><code>--cluster-peers</code></td><td>-</td><td>Comma-separated NATS cluster peer addresses, e.g. node2:6222,node3:6222</td></tr>
<tr><td><code>--nats-cluster-port</code></td><td>6222</td><td>NATS cluster route port for inter-node communication</td></tr>
<tr><td><code>--auth-token</code></td><td>-</td><td>Static bearer token for the SSE/gRPC/sink data plane (prefer <code>KAPTANTO_AUTH_TOKEN</code> env var). <strong>Required</strong> for every network output unless <code>--insecure</code> is set — see below.</td></tr>
<tr><td><code>--insecure</code></td><td>false</td><td>Allow plaintext/unauthenticated SSE, gRPC, and sink observability endpoints without TLS or an auth token — logs a security warning; not for production</td></tr>
<tr><td><code>--tls-cert</code></td><td>-</td><td>Path to TLS certificate PEM for the SSE/gRPC server</td></tr>
<tr><td><code>--tls-key</code></td><td>-</td><td>Path to TLS private key PEM for the SSE/gRPC server</td></tr>
<tr><td><code>--tls-client-ca</code></td><td>-</td><td>Path to CA PEM for client certificate verification (mTLS); requires <code>--tls-cert</code> and <code>--tls-key</code></td></tr>
<tr><td><code>--all-tables</code></td><td>false</td><td>Capture all tables in the database — explicit opt-in required when <code>--tables</code> is omitted</td></tr>
</tbody></table>
<div class="dcall"><p><strong>Startup auth policy:</strong> every network output — <code>sse</code>, <code>grpc</code>, and all five queue sinks — refuses to start without <code>--auth-token</code> (or <code>KAPTANTO_AUTH_TOKEN</code>) because each one serves <code>/metrics</code> and <code>/healthz</code> on <code>--port</code>. <code>sse</code> and <code>grpc</code> additionally require <code>--tls-cert</code>/<code>--tls-key</code>. Pass <code>--insecure</code> to bypass both checks for local development only.</p></div>

<h2 class="dh2">YAML config (full example)</h2>
<div class="dcode">source: postgres://user:pass@host:5432/db
output: kafka
data-dir: /var/lib/kaptanto
retention: 4h
ha: true
node-id: node-1
<span class="tc"># auth-token is intentionally omitted here — set KAPTANTO_AUTH_TOKEN in the</span>
<span class="tc"># process environment instead of committing a token to the config file</span>

tables:
  orders:
    columns: [id, status, amount]
    where: "status != 'archived'"
  users: {}

sinks:
  kafka:
    bootstrap-servers: [broker1:9092, broker2:9092]
    topic-template: "cdc.{{.Schema}}.{{.Table}}"
    sasl-mechanism: PLAIN
    sasl-username: kaptanto
    sasl-password: secret
    tls:
      ca-file: /etc/ssl/kafka-ca.pem</div>

<h2 class="dh2">CLI flags always win</h2>
<p class="dp">CLI flags take precedence over YAML config values. This lets you use a shared config file and override specific settings per environment without editing the file.</p>

<h2 class="dh2">See also</h2>
<div class="dcards">
<div class="dcard" onclick="go('docs-queue-sinks')"><h4>Queue Sinks</h4><p>YAML reference for NATS, SQS, Kafka, Pub/Sub, and RabbitMQ sinks.</p></div>
<div class="dcard" onclick="go('docs-cluster')"><h4>Cluster Mode</h4><p>Active-active delivery with embedded NATS JetStream.</p></div>
</div>`},

'docs-filtering': {title:'Filtering',sub:'Control which events reach your consumers.',body:`
<h2 class="dh2">Table filtering</h2>
<p class="dp">Specify which tables to capture with <code>--tables</code> or as keys under <code>tables:</code> in the YAML config (the config is a map keyed by table name).</p>

<h2 class="dh2">Column filtering</h2>
<p class="dp">Restrict which columns appear in each event with a per-table allow-list:</p>
<div class="dcode">tables:
  users:
    columns: [id, email, status, created_at]   # exclude PII columns</div>

<h2 class="dh2">Row filtering (WHERE condition)</h2>
<div class="dcode">tables:
  orders:
    where: "status != 'draft' AND amount > 0"</div>
<p class="dp">Row filters are evaluated in-process against each change event before delivery to SSE/gRPC consumers.</p>

<h2 class="dh2">Operation filtering</h2>
<p class="dp">Operation filtering is a consumer-side subscription filter, not table config. Pass <code>operations</code> as a query parameter when subscribing over SSE (or in the gRPC <code>SubscribeRequest</code>):</p>
<div class="dcode">GET http://localhost:7654/events?tables=audit_log&operations=insert
<span class="tc"># only inserts — updates and deletes are not delivered</span></div>`},

'docs-grouping': {title:'Message Grouping',sub:'Configure how events are partitioned for ordering.',body:`
<h2 class="dh2">Grouping by primary key</h2>
<p class="dp">Events are grouped by primary key. All events for the same row are delivered in order within their partition, and the 64 partitions are processed in parallel for throughput.</p>
<div class="dcall"><p><strong>Roadmap:</strong> Custom grouping keys — for example, grouping all rows of a child table by a parent <code>order_id</code> — are not yet exposed via flag or YAML. The grouping key is fixed to the primary key today. The tradeoff when it ships: coarse-grained grouping (e.g. <code>account_id</code>) reduces parallelism; fine-grained grouping (primary key) maximizes throughput.</p></div>`},

'docs-ha': {title:'High Availability',sub:'Run kaptanto in HA mode with automatic failover.',body:`
<h2 class="dh2">Agent failover</h2>
<p class="dp">Run two kaptanto instances against the same source. Only one actively consumes via Postgres advisory lock leader election.</p>
<div class="dcode"><span class="tc"># Instance 1</span>
<span class="tg">$</span> kaptanto --source postgres://... --ha --node-id node-1

<span class="tc"># Instance 2 (standby)</span>
<span class="tg">$</span> kaptanto --source postgres://... --ha --node-id node-2</div>

<h2 class="dh2">How it works</h2>
<ul class="dul">
<li>Both instances attempt <code>pg_try_advisory_lock</code> on the source database</li>
<li>The winner starts consuming. The other polls every 5 seconds.</li>
<li>If the primary crashes, its TCP connection drops, the lock releases automatically.</li>
<li>The standby acquires the lock, loads the shared checkpoint, and resumes.</li>
<li>Failover time: approximately 5-10 seconds.</li>
</ul>
<div class="dcall"><p><strong>Why advisory locks:</strong> Session-scoped. No TTL, no clock skew, no split-brain. Released instantly when the connection closes.</p></div>

<h2 class="dh2">Database failover</h2>
<p class="dp">Use multi-host DSNs for automatic primary detection:</p>
<div class="dcode">postgres://host1:5432,host2:5432/mydb?target_session_attrs=read-write</div>`},

'docs-metrics': {title:'Metrics and Monitoring',sub:'Prometheus metrics and health check endpoints.',body:`
<h2 class="dh2">Prometheus endpoint</h2>
<p class="dp">There is no separate metrics flag or port. <code>/metrics</code> and <code>/healthz</code> are served by the output server and follow <code>--port</code>:</p>
<table class="dtbl"><thead><tr><th>Output mode</th><th>Metrics / health bind</th></tr></thead><tbody>
<tr><td><code>sse</code></td><td><code>--port</code> (shared with <code>/events</code>)</td></tr>
<tr><td><code>nats</code>, <code>sqs</code>, <code>kafka</code>, <code>pubsub</code>, <code>rabbitmq</code></td><td><code>--port</code></td></tr>
<tr><td><code>grpc</code></td><td><code>--port</code> + 1 (gRPC owns <code>--port</code>)</td></tr>
<tr><td><code>stdout</code></td><td>not served</td></tr>
</tbody></table>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output sse --port 7654 --insecure
<span class="tc"># GET http://localhost:7654/metrics</span></div>

<h2 class="dh2">Key metrics</h2>
<table class="dtbl"><thead><tr><th>Metric</th><th>Type</th><th>Description</th></tr></thead><tbody>
<tr><td><code>kaptanto_source_lag_bytes</code></td><td>Gauge</td><td>WAL replication lag per source</td></tr>
<tr><td><code>kaptanto_events_delivered_total</code></td><td>Counter</td><td>Events delivered by consumer, table, operation</td></tr>
<tr><td><code>kaptanto_consumer_lag_events</code></td><td>Gauge</td><td>Events behind per consumer</td></tr>
<tr><td><code>kaptanto_errors_total</code></td><td>Counter</td><td>Errors by consumer and kind</td></tr>
<tr><td><code>kaptanto_checkpoint_flushes_total</code></td><td>Counter</td><td>Source checkpoint flushes</td></tr>
<tr><td><code>queue_publish_total</code></td><td>Counter</td><td>Queue-sink publishes by sink</td></tr>
<tr><td><code>queue_publish_errors_total</code></td><td>Counter</td><td>Queue-sink publish errors by sink</td></tr>
<tr><td><code>queue_publish_latency_seconds</code></td><td>Histogram</td><td>Queue-sink publish latency by sink</td></tr>
</tbody></table>

<h2 class="dh2">Health check</h2>
<div class="dcode"><span class="tc"># same host:port as /metrics for the mode above</span>
GET http://localhost:7654/healthz
<span class="tc"># 200 = healthy, 503 = unhealthy with diagnostic JSON</span></div>`},

'docs-api': {title:'HTTP Endpoints',sub:'The HTTP surface kaptanto exposes today.',body:`
<p class="dp">Kaptanto runs a single HTTP server alongside the selected output mode. The endpoints below are everything it serves — there is no dynamic management API yet.</p>
<h2 class="dh2">Available endpoints</h2>
<table class="dtbl"><thead><tr><th>Method</th><th>Path</th><th>Mode</th><th>Description</th></tr></thead><tbody>
<tr><td><code>GET</code></td><td>/events</td><td>sse</td><td>Subscribe to the change stream — filters <code>?tables=</code>, <code>?operations=</code>, <code>?consumer=</code></td></tr>
<tr><td><code>GET</code></td><td>/metrics</td><td>sse, grpc, sinks</td><td>Prometheus metrics</td></tr>
<tr><td><code>GET</code></td><td>/healthz</td><td>sse, grpc, sinks</td><td>Health check — 200 healthy, 503 unhealthy</td></tr>
</tbody></table>
<p class="dp">In SSE and queue-sink modes these bind to <code>--port</code>; in gRPC mode <code>/metrics</code> and <code>/healthz</code> bind to <code>--port</code> + 1. stdout mode serves no HTTP endpoints. See <a onclick="go('docs-sse')">Server-Sent Events</a> and <a onclick="go('docs-metrics')">Metrics &amp; Monitoring</a>.</p>

<h2 class="dh2">Roadmap: management API</h2>
<p class="dp">A REST API for managing sources, tables, and backfills at runtime is not yet implemented. Today, sources and tables are fixed at startup via <code>--source</code>/<code>--tables</code> or the YAML config, and changing them requires a restart. Programmatic management (<code>/api/sources</code>, <code>/api/backfills</code>, …) is planned but not available in the current binary.</p>`},

'docs-troubleshooting': {title:'Troubleshooting',sub:'Common issues and how to resolve them.',body:`
<h2 class="dh2">WAL bloat</h2>
<p class="dp">If kaptanto falls behind, Postgres retains WAL indefinitely. Monitor <code>kaptanto_source_lag_bytes</code> and set an alert in your observability stack when it rises. There is no <code>wal_lag_alert_threshold</code> config field — alerting lives outside kaptanto.</p>

<h2 class="dh2">Slot does not exist</h2>
<p class="dp">After a failover, the replication slot may not exist on the new primary. Kaptanto detects this automatically, creates a new slot, and triggers a re-snapshot.</p>

<h2 class="dh2">TOAST column missing data</h2>
<p class="dp">Set <code>REPLICA IDENTITY FULL</code> on tables with large columns. Without it, unchanged TOAST columns appear as null in update events.</p>

<h2 class="dh2">Consumer falling behind</h2>
<p class="dp">Check <code>kaptanto_consumer_lag_events</code>. There is no built-in slow-consumer disconnect policy — if a consumer consistently falls behind, disconnect it at the application level and reconnect with the same consumer ID to resume from where it left off.</p>`},


'docs-queue-sinks': {title:'Queue Sinks',sub:'Push CDC events to NATS, SQS, Kafka, Pub/Sub, or RabbitMQ.',body:`
<p class="dp">Queue sinks let kaptanto publish each CDC event to a message broker instead of (or in addition to) serving SSE or gRPC consumers. At-least-once delivery is guaranteed. Per-table topic/subject/queue routing is supported on every sink via Go templates.</p>
<div class="dcall"><p><strong>Auth required:</strong> every sink below exposes <code>/metrics</code> and <code>/healthz</code> on <code>--port</code>, so kaptanto refuses to start without <code>--auth-token</code> (or <code>KAPTANTO_AUTH_TOKEN</code>) unless you pass <code>--insecure</code> — see <a onclick="go('docs-config')">Configuration</a>.</p></div>

<h2 class="dh2">NATS JetStream</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output nats --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcode">sinks:
  nats:
    url: nats://localhost:4222
    subject-template: "cdc.{{.Schema}}.{{.Table}}"
    stream-name: CDC          <span class="tc"># optional; validated at startup if set</span>
    tls:
      ca-file: /etc/ssl/nats-ca.pem
      cert-file: /etc/ssl/client-cert.pem   <span class="tc"># mTLS</span>
      key-file:  /etc/ssl/client-key.pem</div>
<p class="dp">Events are published to the NATS JetStream subject derived from the template. The subject must fall within the stream's subject filter. If <code>stream-name</code> is set, kaptanto verifies the stream exists at startup and returns an error if not.</p>

<h2 class="dh2">AWS SQS (FIFO)</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output sqs --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcode">sinks:
  sqs:
    region: us-east-1
    queue-url: https://sqs.us-east-1.amazonaws.com/123456789/cdc.fifo
    <span class="tc"># OR route each table to its own FIFO queue:</span>
    queue-url-template: "https://sqs.us-east-1.amazonaws.com/123456789/cdc-{{.Schema}}-{{.Table}}.fifo"
    access-key-id: AKIA...         <span class="tc"># optional; uses IAM role if omitted</span>
    secret-access-key: ...
    tls:
      ca-file: /etc/ssl/vpc-ca.pem   <span class="tc"># useful for VPC endpoints</span>
      cert-file: /etc/ssl/client.pem  <span class="tc"># mTLS</span>
      key-file:  /etc/ssl/client.key</div>
<p class="dp">Kaptanto uses FIFO queues with <code>MessageGroupId</code> set to the primary key, preserving per-key ordering. <code>queue-url-template</code> takes precedence over <code>queue-url</code> when both are set. Queue connections are pooled per resolved URL.</p>
<div class="dcall"><p><strong>High-throughput FIFO mode</strong> is a queue-level AWS setting that does not require any config change in kaptanto. Enable it on the queue in the AWS console to exceed 300 TPS.</p></div>

<h2 class="dh2">Apache Kafka</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output kafka --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcode">sinks:
  kafka:
    bootstrap-servers: [broker1:9092, broker2:9092]
    topic-template: "cdc.{{.Schema}}.{{.Table}}"
    sasl-mechanism: SCRAM-SHA-256   <span class="tc"># PLAIN | SCRAM-SHA-256 | SCRAM-SHA-512 | "" (none)</span>
    sasl-username: kaptanto
    sasl-password: secret
    tls:
      ca-file: /etc/ssl/kafka-ca.pem
      cert-file: /etc/ssl/client.pem
      key-file:  /etc/ssl/client.key</div>
<p class="dp">The event primary key is used as the Kafka message key, so partitioning by key is consistent with kaptanto's per-key ordering guarantee. Create topics in advance or enable auto-topic creation on the broker.</p>

<h2 class="dh2">Google Cloud Pub/Sub</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output pubsub --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcode">sinks:
  pubsub:
    project-id: my-gcp-project
    topic-id: cdc-events          <span class="tc"># used when topic-template is empty</span>
    topic-template: "cdc-{{.Schema}}-{{.Table}}"  <span class="tc"># optional; per-table routing</span>
    credentials-file: /etc/gcp/sa-key.json  <span class="tc"># optional; uses ADC if omitted</span></div>
<p class="dp">When <code>credentials-file</code> is omitted, Application Default Credentials are used — set <code>GOOGLE_APPLICATION_CREDENTIALS</code> or run <code>gcloud auth application-default login</code>. Publishers are lazily created and pooled per resolved topic.</p>

<h2 class="dh2">RabbitMQ</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output rabbitmq --auth-token "$KAPTANTO_AUTH_TOKEN"</div>
<div class="dcode">sinks:
  rabbitmq:
    url: amqp://user:pass@broker:5672/
    exchange: cdc-exchange         <span class="tc"># empty string = default exchange</span>
    routing-key-template: "cdc.{{.Schema}}.{{.Table}}"
    tls:
      ca-file: /etc/ssl/rabbit-ca.pem
      cert-file: /etc/ssl/client.pem   <span class="tc"># mTLS</span>
      key-file:  /etc/ssl/client.key</div>
<p class="dp">Kaptanto uses publisher confirms — delivery is not acknowledged until RabbitMQ confirms persistence. On connection failure, the sink reconnects automatically with exponential backoff.</p>

<h2 class="dh2">Template variables</h2>
<table class="dtbl"><thead><tr><th>Variable</th><th>Value</th><th>Example</th></tr></thead><tbody>
<tr><td><code>{{.Schema}}</code></td><td>Table schema</td><td>public</td></tr>
<tr><td><code>{{.Table}}</code></td><td>Table name</td><td>orders</td></tr>
<tr><td><code>{{.Operation}}</code></td><td>Event operation</td><td>insert</td></tr>
</tbody></table>

<h2 class="dh2">TLS reference</h2>
<table class="dtbl"><thead><tr><th>Field</th><th>Purpose</th></tr></thead><tbody>
<tr><td><code>ca-file</code></td><td>Custom CA certificate for broker TLS verification (useful for private CAs or VPC endpoints)</td></tr>
<tr><td><code>cert-file</code></td><td>Client certificate for mutual TLS (mTLS)</td></tr>
<tr><td><code>key-file</code></td><td>Client private key for mutual TLS (mTLS)</td></tr>
</tbody></table>
<p class="dp">All TLS fields are optional. Omit the entire <code>tls:</code> block to use system CAs without mTLS.</p>`},

'docs-cluster': {title:'Cluster Mode',sub:'Active-active delivery across multiple kaptanto nodes with embedded NATS JetStream.',body:`
<p class="dp">Cluster mode runs multiple kaptanto nodes in an active-active configuration. Each node owns a subset of the 64 partitions and delivers events for those partitions. If a node fails, its partitions are claimed by remaining nodes within seconds.</p>
<div class="dcall"><p><strong>Requires:</strong> Cluster mode uses an embedded NATS JetStream server for the distributed Event Log and a shared Postgres database for cursor and backfill state. No separate NATS installation is needed.</p></div>

<h2 class="dh2">Quick start (3-node cluster)</h2>
<p class="dp">Like every network output, SSE refuses to start without TLS certificates and an auth token unless <code>--insecure</code> is set — see <a onclick="go('docs-config')">Configuration</a>.</p>
<div class="dcode"><span class="tc"># Node 1</span>
<span class="tg">$</span> kaptanto \\
    --source postgres://... \\
    --output sse \\
    --tls-cert server.pem --tls-key server.key --auth-token "$KAPTANTO_AUTH_TOKEN" \\
    --cluster \\
    --node-id node-1 \\
    --cluster-dsn postgres://user:pass@shared-pg:5432/kaptanto \\
    --cluster-peers node2:6222,node3:6222

<span class="tc"># Node 2</span>
<span class="tg">$</span> kaptanto \\
    --source postgres://... \\
    --output sse \\
    --tls-cert server.pem --tls-key server.key --auth-token "$KAPTANTO_AUTH_TOKEN" \\
    --cluster \\
    --node-id node-2 \\
    --cluster-dsn postgres://user:pass@shared-pg:5432/kaptanto \\
    --cluster-peers node1:6222,node3:6222

<span class="tc"># Node 3 (same pattern)</span></div>

<h2 class="dh2">How it works</h2>
<ul class="dul">
<li><strong>Distributed Event Log</strong> — Each node runs an embedded NATS JetStream server. The three servers form a JetStream cluster and replicate the Event Log across all nodes.</li>
<li><strong>Partition ownership</strong> — A <code>PartitionStore</code> (backed by the shared Postgres DSN) tracks which node owns each of the 64 partitions. Nodes claim partitions on startup and heartbeat to retain them.</li>
<li><strong>Epoch fencing</strong> — Each ownership era is assigned a monotonically increasing epoch. A node that reconnects after a partition is stolen cannot deliver stale events to consumers — its WAL standby status message is fenced by the epoch check.</li>
<li><strong>Shared cursor state</strong> — Consumer delivery positions are stored in Postgres (<code>--cluster-dsn</code>) so any node can resume a consumer after a failover.</li>
<li><strong>Leader election for WAL</strong> — Only one node replicates from the Postgres WAL at a time. A <code>WalLeaderElector</code> uses NATS JetStream KV with a TTL lease (refreshed on each heartbeat). If the current WAL leader stops heartbeating, another node acquires the lease and begins replication.</li>
</ul>

<h2 class="dh2">Configuration reference</h2>
<table class="dtbl"><thead><tr><th>Flag</th><th>Default</th><th>Description</th></tr></thead><tbody>
<tr><td><code>--cluster</code></td><td>false</td><td>Enable cluster mode</td></tr>
<tr><td><code>--cluster-dsn</code></td><td>required</td><td>Postgres DSN for shared cursor, backfill, and partition state</td></tr>
<tr><td><code>--cluster-peers</code></td><td>-</td><td>NATS route addresses of peer nodes, e.g. <code>node2:6222,node3:6222</code></td></tr>
<tr><td><code>--nats-cluster-port</code></td><td>6222</td><td>Port this node listens on for NATS cluster routes</td></tr>
<tr><td><code>--node-id</code></td><td>auto</td><td>Unique identifier for this node in the partition table</td></tr>
</tbody></table>

<h2 class="dh2">YAML config</h2>
<div class="dcode">source: postgres://user:pass@primary:5432/mydb
output: sse
cluster: true
cluster-dsn: postgres://user:pass@shared-pg:5432/kaptanto
cluster-peers: [node2:6222, node3:6222]
nats-cluster-port: 6222
node-id: node-1</div>

<h2 class="dh2">Failure scenarios</h2>
<table class="dtbl"><thead><tr><th>Event</th><th>Behavior</th></tr></thead><tbody>
<tr><td>WAL leader crashes</td><td>JetStream KV TTL expires; another node wins the lease and resumes WAL replication from last checkpoint</td></tr>
<tr><td>Delivery node crashes</td><td>Partition ownership TTL expires; surviving nodes steal orphaned partitions and resume delivery</td></tr>
<tr><td>Network partition</td><td>Epoch fencing prevents the isolated node from delivering stale events; it resigns ownership on reconnect</td></tr>
<tr><td>Postgres DSN unreachable</td><td>Cluster state reads fail; node stops accepting new partition claims until connectivity is restored</td></tr>
</tbody></table>

<h2 class="dh2">vs. HA mode</h2>
<p class="dp"><code>--ha</code> (Postgres advisory lock) is active-passive — one node captures and delivers, the other is a hot standby. <code>--cluster</code> is active-active — all nodes deliver events concurrently for different partitions. Use HA for simplicity; use cluster when you need horizontal throughput scaling.</p>`},

'docs-aws-setup': {title:'AWS Deployment Guide',sub:'How to run kaptanto, Debezium, and Sequin alongside an API on AWS — and what each setup actually costs you.',body:`
<div class="dcall"><p><strong>Scenario:</strong> An order management API (Node.js / Python / any language) running on ECS Fargate, backed by RDS Postgres. Every row written via the API must be streamed to downstream consumers in real time.</p></div>
<h2 class="dh2">Best use cases for kaptanto</h2>
<ul class="dul">
<li><strong>Notification pipelines</strong> — new order row → push notification fan-out in &lt;2s</li>
<li><strong>Live search sync</strong> — product catalog changes → Elasticsearch/Typesense index update</li>
<li><strong>Cache invalidation</strong> — row update → Redis key eviction before the next read</li>
<li><strong>Audit trail</strong> — every write captured and appended to an append-only audit log</li>
</ul>
<p class="dp">These patterns share a common shape: low-to-medium write rate (&lt;5k eps steady state), consumers that need events within 2–5s, and no requirement for exactly-once semantics or petabyte-scale sink connectors.</p>

<h2 class="dh2">The common baseline</h2>
<p class="dp">All tools read from the same Postgres source. You need logical replication enabled on RDS. That is the only change common to every option:</p>
<div class="dcode"><span class="tc">-- RDS parameter group (requires reboot)</span>
rds.logical_replication = 1
max_replication_slots   = 10
max_wal_senders         = 10</div>
<p class="dp">Your API writes normally — no CDC-specific code in the application layer:</p>
<div class="dcode"><span class="tc">// order-service.js — nothing CDC-specific here</span>
await db.query(
  <span class="ty">'INSERT INTO orders (id, status, amount) VALUES ($1, $2, $3)'</span>,
  [orderId, <span class="ty">'pending'</span>, amount]
);
<span class="tc">// kaptanto picks up the WAL event automatically</span></div>

<h2 class="dh2">Option 1 — kaptanto on ECS Fargate</h2>
<p class="dp"><strong>Infrastructure required:</strong> One additional ECS Fargate task. Nothing else.</p>
<div class="dcode"><span class="tc"># task definition (simplified)</span>
{
  "name": "kaptanto",
  "image": "olucasandrade/kaptanto:latest",
  "command": [
    "--source", "postgres://api_user:pass@rds-host:5432/orders",
    "--tables", "orders,payments",
    "--output", "sse",
    "--port",   "7654",
    "--data-dir", "/data",
    "--tls-cert", "/certs/server.pem",
    "--tls-key",  "/certs/server.key"
  ],
  "secrets": [{ "name": "KAPTANTO_AUTH_TOKEN", "valueFrom": "arn:aws:secretsmanager:...:secret:kaptanto-auth-token" }],
  "mountPoints": [{ "sourceVolume": "kaptanto-data", "containerPath": "/data" }]
}</div>
<div class="dcall"><p><strong>Required:</strong> SSE refuses to start without both TLS certificates and an auth token — the task definition above mounts a cert pair and injects <code>KAPTANTO_AUTH_TOKEN</code> from Secrets Manager. See <a onclick="go('docs-config')">Configuration</a> for the full startup policy.</p></div>
<p class="dp">Consumers anywhere in the VPC subscribe over HTTP with a bearer token. No broker, no queue, no extra AWS service:</p>
<div class="dcode"><span class="tc">// inventory-service.js — consuming kaptanto SSE</span>
import EventSource from <span class="ty">'eventsource'</span>;

const es = new EventSource(
  <span class="ty">'https://kaptanto.internal:7654/events?consumer=inventory'</span>,
  { headers: { Authorization: <span class="ty">'Bearer ' + process.env.KAPTANTO_AUTH_TOKEN</span> } }
);

es.onmessage = (e) =&gt; {
  const evt = JSON.parse(e.data);
  if (evt.operation === <span class="ty">'insert'</span> &amp;&amp; evt.table === <span class="ty">'orders'</span>) {
    reserveInventory(evt.after.sku, evt.after.qty);
  }
};</div>
<div class="dcall"><p><strong>Instance sizing:</strong> kaptanto uses ~563 MB RSS at steady load, peaking ~1.0 GB during crash recovery. Allocate at least 1 vCPU / 4 GB memory. A 0.25 vCPU / 0.5 GB task will OOM under production traffic.</p></div>
<p class="dp"><strong>AWS cost:</strong> ~$85/mo for a 1 vCPU / 4 GB Fargate task (t3.medium equivalent — minimum viable to cover the ~1.0 GB crash-recovery peak). EFS volume for the event log (~$0.30/GB/mo). Total overhead: roughly <strong>$85–100/mo</strong> with EFS.</p>
<div class="dcall"><p><strong>Failure model:</strong> If kaptanto restarts, it replays from its last checkpoint. The consumer reconnects with the same consumer ID and resumes from where it left off. No events lost.</p></div>

<h2 class="dh2">Option 2 — kaptanto with Rust FFI acceleration</h2>
<p class="dp">There is no published <code>kaptanto-rust</code> Docker image today — the Rust FFI parser requires <code>CGO_ENABLED=1</code> and a host Rust toolchain (<code>make build-rust</code>), so it is not part of the cross-compiled release pipeline. To run it on Fargate, build a custom image from source in your own CI:</p>
<div class="dcode"><span class="tc"># Dockerfile — build stage, requires Rust 1.77+ and cbindgen</span>
FROM golang:1.25 AS build
RUN apt-get update &amp;&amp; apt-get install -y curl build-essential
RUN curl https://sh.rustup.rs -sSf | sh -s -- -y
ENV PATH="/root/.cargo/bin:${'$'}{PATH}"
WORKDIR /src
COPY . .
RUN make build-rust</div>
<p class="dp">Same operational model as Option 1 once built — same flags, same auth/TLS requirements. In the current benchmark run, the Rust FFI path recovers from crashes faster than the Go parser (3.5s vs. 4.5s) and posts the lowest p50 latency in the burst and cluster scenarios — see the <a onclick="go('docs-benchmarks')">Benchmarks</a> comparison table below. Its steady-state throughput and RSS are slightly worse than plain kaptanto (~1,993 vs. ~2,645 eps steady, ~661 vs. ~563 MB), so evaluate it for latency and recovery speed, not raw steady throughput.</p>

<h2 class="dh2">Option 3 — Debezium on ECS + Amazon MSK</h2>
<p class="dp"><strong>Infrastructure required:</strong> Amazon MSK cluster, Kafka Connect cluster (ECS or MSK Connect), Debezium connector config, Schema Registry (optional but typical).</p>
<div class="dcode"><span class="tc"># MSK cluster — minimum viable (2 brokers, kafka.m5.large)</span>
<span class="tc"># Cost: ~$200/mo before storage and data transfer</span>

<span class="tc"># debezium connector config (registered via Kafka Connect REST API)</span>
{
  "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
  "database.hostname": "rds-host",
  "database.port":     "5432",
  "database.user":     "debezium_user",
  "database.password": "...",
  "database.dbname":   "orders",
  "table.include.list": "public.orders,public.payments",
  "plugin.name":       "pgoutput",
  "topic.prefix":      "prod"
}</div>
<p class="dp">Your consumer is a standard Kafka consumer group:</p>
<div class="dcode"><span class="tc"># Python consumer</span>
from confluent_kafka import Consumer

c = Consumer({
    <span class="ty">'bootstrap.servers'</span>: <span class="ty">'msk-broker:9092'</span>,
    <span class="ty">'group.id'</span>:          <span class="ty">'inventory-service'</span>,
    <span class="ty">'auto.offset.reset'</span>: <span class="ty">'earliest'</span>
})
c.subscribe([<span class="ty">'prod.public.orders'</span>])

while True:
    msg = c.poll(1.0)
    if msg and not msg.error():
        event = json.loads(msg.value())
        reserve_inventory(event[<span class="ty">'after'</span>][<span class="ty">'sku'</span>], event[<span class="ty">'after'</span>][<span class="ty">'qty'</span>])
        c.commit()   <span class="tc"># explicit commit = at-least-once</span></div>
<p class="dp"><strong>AWS cost:</strong> MSK (2× m5.large) ~$200/mo + MSK Connect ~$50/mo + EBS storage + data transfer. Total overhead: <strong>$300–500/mo minimum</strong>, scaling with volume.</p>
<div class="dcall"><p><strong>When this is the right call:</strong> Your organization already runs MSK or Kafka for other workloads. The $300/mo is already paid. You need connector ecosystem coverage — SMTs, Schema Registry, dead-letter queues, Snowflake/S3 sinks — and your team has Kafka expertise. Do not build this from scratch just for CDC.</p></div>

<h2 class="dh2">Option 4 — Sequin on ECS</h2>
<p class="dp"><strong>Infrastructure required:</strong> ECS service for Sequin, ElastiCache Redis, a second RDS instance (Sequin metadata DB), or use Sequin Cloud.</p>
<div class="dcode"><span class="tc"># sequin.yml — mounted into the Sequin container</span>
account:
  name: production

databases:
  - name: orders-db
    hostname: rds-host
    port: 5432
    database: orders
    username: sequin_user
    password: "..."
    slot_name: sequin_slot
    publication_name: sequin_pub

http_push_consumers:
  - name: inventory-webhook
    stream_name: orders
    http_endpoint: https://inventory.internal/cdc/orders
    retry_policy:
      max_attempts: 10
      initial_delay_ms: 500</div>
<p class="dp">Your consumer is a plain HTTP endpoint — no persistent connection, no client library:</p>
<div class="dcode"><span class="tc">// Express.js — Sequin pushes to this endpoint</span>
app.post(<span class="ty">'/cdc/orders'</span>, async (req, res) =&gt; {
  const { record, action } = req.body;
  if (action === <span class="ty">'insert'</span>) {
    await reserveInventory(record.sku, record.qty);
  }
  res.sendStatus(200); <span class="tc">// ACK — Sequin marks as delivered</span>
                       <span class="tc">// non-2xx = Sequin retries with backoff</span>
});</div>
<p class="dp"><strong>AWS cost:</strong> ElastiCache (cache.t3.micro) ~$15/mo + second RDS (db.t3.micro) ~$25/mo + ECS task ~$10/mo. Or Sequin Cloud free tier for low volume. Total self-hosted overhead: <strong>$50–80/mo</strong>.</p>
<div class="dcall"><p><strong>When this is the right call:</strong> Your team wants HTTP webhook semantics — Sequin pushes to your endpoint, handles retries, and your service just needs to respond 200. Best for product teams who don't want to manage a replication slot or write a CDC consumer. Not suitable above ~500 events/sec.</p></div>

<h2 class="dh2">Side-by-side comparison</h2>
<div class="cw" style="overflow-x:auto;margin:1.5rem 0">
<table class="cmp">
<thead><tr><th>Dimension</th><th>kaptanto</th><th>kaptanto-rust</th><th>Debezium + MSK</th><th>Sequin</th></tr></thead>
<tbody>
<tr><td>Extra AWS services</td><td class="ck">None</td><td class="ck">None</td><td class="cx">MSK + Connect</td><td class="ca">Redis + RDS</td></tr>
<tr><td>Monthly overhead</td><td class="ck">~$85</td><td class="ck">~$85</td><td class="cx">$300–500+</td><td class="ca">$50–80</td></tr>
<tr><td>Consumer protocol</td><td>SSE / gRPC</td><td>SSE / gRPC</td><td>Kafka</td><td>HTTP push</td></tr>
<tr><td>Throughput ceiling</td><td>~2.6k eps steady / 3.3k eps peak</td><td>~2.0k eps steady / 3.5k eps peak</td><td>Kafka-bound</td><td class="cx">~70 eps steady / 613 peak</td></tr>
<tr><td>Post-crash drain (p50)</td><td class="ck">4.5s recovery</td><td class="ck">3.5s recovery</td><td class="cx">3.4s recovery</td><td class="cx">4.4s to re-sync</td></tr>
<tr><td>Consumer reconnects</td><td>Auto, by ID</td><td>Auto, by ID</td><td>Kafka group</td><td>Retry queue</td></tr>
<tr><td>Delivery guarantee</td><td>At-least-once</td><td>At-least-once</td><td class="ck">Exactly-once¹</td><td>At-least-once</td></tr>
<tr><td>Team expertise needed</td><td>Go/HTTP</td><td>Go/HTTP</td><td class="cx">Kafka ops</td><td>HTTP</td></tr>
<tr><td>IAM / security surface</td><td>Minimal</td><td>Minimal</td><td class="cx">Large (MSK)</td><td>Medium</td></tr>
</tbody>
</table>
<p class="dp" style="font-size:.8rem;margin-top:.5rem">¹ Requires Kafka transactions + consumer idempotent commit discipline</p>
</div>

<h2 class="dh2">Which to choose</h2>
<ul class="dul">
<li><strong>kaptanto</strong> — you're starting from scratch, want the lowest operational cost, and your consumers can hold a persistent HTTP connection. Covers 95% of use cases. Use the official Docker image as-is.</li>
<li><strong>kaptanto with Rust FFI</strong> — same as above, but you want the faster crash recovery and lower burst/cluster p50 latency of the Rust pgoutput decoder (see <a onclick="go('docs-benchmarks')">Benchmarks</a>) and are willing to build and maintain your own image from source, since no prebuilt Rust image is published.</li>
<li><strong>Debezium + MSK</strong> — your organization already pays for MSK and has Kafka-literate engineers. You need Kafka sink connectors (Snowflake, S3, BigQuery) or exactly-once semantics. Never set this up solely for CDC.</li>
<li><strong>Sequin</strong> — a product team that wants webhook-style delivery and will never exceed ~500 events/sec. The simplest possible consumer integration: just respond 200.</li>
</ul>
<div class="dcards">
<div class="dcard" onclick="go('docs-consistency')"><h4>Consistency Model</h4><p>Delivery guarantees, durability, and what happens on crash.</p></div>
<div class="dcard" onclick="go('docs-ha')"><h4>High Availability</h4><p>Leader election and multi-AZ failover.</p></div>
</div>`},

'docs-benchmarks': {title:'Benchmarks',sub:'Throughput and latency results vs. Debezium, Sequin, and PeerDB across 5 scenarios.',body:`
<p class="dp">Tested on GitHub Actions ubuntu-latest (4 vCPU, 16 GB RAM), Postgres 16, 5 CDC scenarios, 2026-07-04. Eight tools ran concurrently from the same database; all numbers reflect shared-CPU conditions.</p>
<div class="dcall"><p><strong>Reproducing this run:</strong> the harness lives in <code>bench/</code> and is driven by <code>docker compose</code> plus <code>go run ./cmd/scenarios</code> — see the repo's <code>bench/README.md</code>. <code>bench/results/REPORT.md</code> in the repository tracks the most recent local run for development purposes and may show different numbers than the CI run summarized here; treat the table below as the current published result.</p></div>

<h2 class="dh2">Executive Summary</h2>
<div class="bench-winners">
<div class="bench-win bench-win-top"><span>Overall fit</span><strong>kaptanto</strong><em>Fastest steady throughput, smallest operating shape</em></div>
<div class="bench-win"><span>Steady throughput</span><strong>2,645 eps</strong><em>kaptanto</em></div>
<div class="bench-win"><span>Peak throughput</span><strong>3,522 eps</strong><em>kaptanto-rust</em></div>
<div class="bench-win"><span>Burst latency</span><strong>2.3 s p50</strong><em>kaptanto-rust</em></div>
<div class="bench-win"><span>Recovery</span><strong>3.4 s</strong><em>Debezium HTTP sink</em></div>
</div>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady (eps)</th><th>Peak (eps)</th><th>p50 Burst Latency</th><th>Recovery</th><th>Infrastructure</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td class="bw"><strong>2,645</strong><span>Winner</span></td><td><strong>3,256</strong></td><td>3.5 s</td><td>4.5 s</td><td class="bw bw-soft">1 binary (Go) · ~563 MB RSS<span>Simplest</span></td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td><strong>1,993</strong></td><td class="bw"><strong>3,522</strong><span>Winner</span></td><td class="bw">2.3 s<span>Winner</span></td><td>3.5 s</td><td>1 binary (Go+Rust FFI) · ~661 MB RSS</td></tr>
<tr><td><strong>kaptanto-kafka</strong></td><td><strong>2,560</strong></td><td><strong>3,048</strong></td><td>8.5 s</td><td>—</td><td>1 binary + Redpanda · ~563 MB RSS</td></tr>
<tr><td><strong>kaptanto-nats</strong></td><td><strong>2,409</strong></td><td><strong>3,232</strong></td><td>3.9 s</td><td>—</td><td>1 binary + NATS JetStream · ~575 MB RSS</td></tr>
<tr><td>PeerDB</td><td>2,543</td><td>3,104</td><td>8.2 s</td><td>4.7 s</td><td>4 Go services + Temporal + Kafka + PG</td></tr>
<tr><td>Debezium (Kafka Connect)</td><td>951</td><td>2,584</td><td>12.2 s</td><td>—</td><td>JVM + Kafka Connect + Redpanda</td></tr>
<tr><td>Debezium (HTTP sink)</td><td>100</td><td>1,120</td><td>32.8 s</td><td class="bw bw-alt">3.4 s<span>Winner</span></td><td>JVM only</td></tr>
<tr><td>Sequin</td><td>68</td><td>613</td><td>8.3 s</td><td>4.4 s</td><td>Elixir + Redis + PG</td></tr>
</tbody>
</table></div>
<p class="dp">kaptanto delivers <strong>26× the steady-state throughput</strong> of Debezium HTTP and <strong>39× Sequin</strong> as a single binary, and matches PeerDB across all scenarios with a fraction of the infrastructure.</p>
<div class="dcall"><p><strong>Memory note:</strong> At steady load, kaptanto uses ~563 MB RSS and kaptanto-rust ~661 MB RSS. Peak during crash-recovery reaches ~1.0 GB (~1.2 GB for the Kafka sink). Not edge-suitable — minimum instance is a t3.medium (4 GB). Debezium uses ~336 MB, Sequin ~628 MB.</p></div>

<h2 class="dh2">Throughput by Scenario (eps)</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady</th><th>Burst</th><th>Large Batch</th><th>Crash Recovery</th><th>Cluster</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td class="bw">2,645<span>Winner</span></td><td class="bw">2,315<span>Winner</span></td><td>3,256</td><td class="bw">2,390<span>Winner</span></td><td class="bw">3,019<span>Winner</span></td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>1,993</td><td>2,127</td><td class="bw">3,522<span>Winner</span></td><td>2,115</td><td>2,535</td></tr>
<tr><td><strong>kaptanto-kafka</strong></td><td>2,560</td><td>2,198</td><td>3,048</td><td>2,340</td><td>2,806</td></tr>
<tr><td><strong>kaptanto-nats</strong></td><td>2,409</td><td>2,301</td><td>3,232</td><td>2,305</td><td>2,557</td></tr>
<tr><td>PeerDB</td><td>2,543</td><td>2,244</td><td>3,104</td><td>2,343</td><td>2,800</td></tr>
<tr><td>Debezium (Kafka Connect)</td><td>951</td><td>797</td><td>2,584</td><td>1,149</td><td>718</td></tr>
<tr><td>Debezium (HTTP sink)</td><td>100</td><td>38</td><td>1,120</td><td>69</td><td>52</td></tr>
<tr><td>Sequin</td><td>68</td><td>97</td><td>613</td><td>40</td><td>75</td></tr>
</tbody>
</table></div>
<p class="dp">Throughput is the actual consumer receive rate (events / per-tool receive span), so each tool shows its true drain speed regardless of scenario window duration.</p>

<h2 class="dh2">Latency p50 / p95 / p99 (ms)</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady</th><th>Burst</th><th>Crash Recovery</th><th>Cluster</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>13,710 / 36,193 / 37,735</td><td>3,474 / 9,236 / 11,466</td><td class="bw">27,944 / 77,984 / 81,226<span>p50 winner</span></td><td>3,178 / 12,059 / 12,525</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td class="bw">12,405 / 40,477 / 42,256<span>p50 winner</span></td><td class="bw">2,304 / 11,685 / 13,073<span>p50 winner</span></td><td>33,956 / 87,583 / 91,247</td><td class="bw">1,652 / 12,497 / 13,514<span>p50 winner</span></td></tr>
<tr><td><strong>kaptanto-kafka</strong></td><td>14,933 / 34,696 / 38,398</td><td>8,460 / 12,750 / 13,692</td><td>29,960 / 76,322 / 82,837</td><td>3,077 / 12,476 / 13,069</td></tr>
<tr><td><strong>kaptanto-nats</strong></td><td>17,536 / 36,266 / 39,639</td><td>3,859 / 11,999 / 12,205</td><td>33,797 / 77,537 / 81,361</td><td>3,744 / 12,394 / 13,626</td></tr>
<tr><td>PeerDB</td><td>14,918 / 34,687 / 38,300</td><td>8,176 / 13,154 / 13,431</td><td>29,922 / 76,887 / 82,787</td><td>3,052 / 12,350 / 13,143</td></tr>
<tr><td>Debezium (Kafka Connect)</td><td>34,581 / 52,118 / 52,402</td><td>12,242 / 24,155 / 24,926</td><td>68,607 / 99,967 / 100,804</td><td>19,619 / 27,841 / 28,578</td></tr>
<tr><td>Debezium (HTTP sink)</td><td>43,620 / 58,941 / 62,934</td><td>32,754 / 34,602 / 34,757</td><td>91,452 / 116,688 / 120,275</td><td>21,867 / 34,245 / 35,031</td></tr>
<tr><td>Sequin</td><td>38,224 / 62,564 / 64,005</td><td>8,300 / 31,019 / 32,003</td><td>36,510 / 120,328 / 123,847</td><td>22,545 / 34,065 / 34,835</td></tr>
</tbody>
</table></div>
<p class="dp" style="font-size:.8rem;margin-top:.5rem">Latency winners are based on p50 in each scenario. p95/p99 tails are shown in the same cell for context.</p>

<h2 class="dh2">Crash Recovery Time</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Recovery Time</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>4.5 s</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>3.5 s</td></tr>
<tr><td>PeerDB</td><td>4.7 s</td></tr>
<tr><td>Debezium (HTTP sink)</td><td class="bw bw-alt">3.4 s<span>Winner</span></td></tr>
<tr><td>Sequin</td><td>4.4 s</td></tr>
</tbody>
</table></div>
<p class="dp">All tools recover within ~5 seconds. Crash recovery time measures from process restart to the first event received after re-connection.</p>

<h2 class="dh2">Methodology</h2>
<p class="dp">Five scenarios were run sequentially against the same Postgres 16 instance:</p>
<ul class="dlist">
<li><strong>Steady</strong> — constant low-rate inserts</li>
<li><strong>Burst</strong> — spike of high-rate inserts followed by idle</li>
<li><strong>Large Batch</strong> — bulk insert of 100k rows; measures each tool's actual consumer drain rate</li>
<li><strong>Crash Recovery</strong> — SIGKILL mid-stream, then restart and measure time to caught-up</li>
<li><strong>Cluster</strong> — same workload while kaptanto runs as a two-node active-active cluster with a shared cursor store; other tools run in their standard single-node deployment</li>
</ul>
<p class="dp">Throughput is measured as events-per-second at the consumer (event count / per-tool receive span). Latency is end-to-end: row committed in Postgres to event received by consumer. All eight tools ran concurrently in Docker on a shared 4 vCPU GitHub Actions runner.</p>
<h2 class="dh2">When kaptanto is not the right fit</h2>
<ul class="dul">
<li><strong>Edge / IoT deployments</strong> — ~563 MB RSS at steady load, peaking ~1.0 GB during crash-recovery. Requires at least a t3.medium (4 GB). Not suitable for constrained environments.</li>
<li><strong>Petabyte-scale ETL</strong> — kaptanto streams continuously; it is not an atomic batch system and does not write to S3/Snowflake natively. Use Debezium with Kafka connectors for data warehouse pipelines.</li>
<li><strong>Sub-second p99 SLAs at high volume</strong> — burst p99 is ~11 s under shared-CPU load. If your pipeline requires guaranteed low tail latency under sustained load, evaluate purpose-built streaming systems.</li>
<li><strong>Crash-recovery p99 SLAs</strong> — kaptanto Go p99 crash-recovery latency is ~81 s (kaptanto-rust ~91 s). Accept this tradeoff explicitly if you have tail-latency SLAs during recovery windows.</li>
</ul>`}
};

export const SIDEBAR: Array<{ label: string; items: [string, string][] }> = [
  {label:'Get Started',items:[['docs-intro','Introduction'],['docs-quickstart','Quick Start'],['docs-install','Installation'],['docs-postgres','Connect Postgres'],['docs-mongo','Connect MongoDB']]},
  {label:'Core Concepts',items:[['docs-schema','Event Schema'],['docs-backfills','Backfills'],['docs-consistency','Consistency Model'],['docs-ordering','Ordering & Partitions']]},
  {label:'Output Modes',items:[['docs-stdout','stdout'],['docs-sse','Server-Sent Events'],['docs-grpc','gRPC'],['docs-queue-sinks','Queue Sinks']]},
  {label:'Configuration',items:[['docs-config','CLI & YAML'],['docs-filtering','Filtering'],['docs-grouping','Message Grouping']]},
  {label:'Production',items:[['docs-ha','High Availability'],['docs-cluster','Cluster Mode'],['docs-metrics','Metrics & Monitoring'],['docs-api','HTTP Endpoints'],['docs-troubleshooting','Troubleshooting']]},
  {label:'Guides',items:[['docs-aws-setup','AWS Deployment Guide'],['docs-benchmarks','Benchmarks']]}
];

export const DOC_FLOW = ['docs-intro','docs-quickstart','docs-install','docs-postgres','docs-mongo','docs-schema','docs-backfills','docs-consistency','docs-ordering','docs-stdout','docs-sse','docs-grpc','docs-queue-sinks','docs-config','docs-filtering','docs-grouping','docs-ha','docs-cluster','docs-metrics','docs-api','docs-troubleshooting','docs-aws-setup','docs-benchmarks'];
