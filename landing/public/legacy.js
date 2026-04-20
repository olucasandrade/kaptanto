// ── DOCS DATA ──
var docs = {
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

<span class="tc"># Events stream as newline-delimited JSON</span>
{"op":"insert","table":"orders","after":{"id":1,"status":"pending"}}
{"op":"update","table":"orders","before":{"status":"pending"},"after":{"status":"paid"}}</div>
<p class="dp">It runs as a single binary with zero external dependencies. No Kafka, no ZooKeeper, no JVM. Handles backfills, checkpointing, per-key ordering, and database failover natively.</p>
<div class="dcall"><p><strong>Critical invariant:</strong> The source checkpoint is never advanced until the event is durably written to the internal Event Log. If kaptanto crashes, the source re-sends from the last acknowledged position. Zero events lost.</p></div>
<h2 class="dh2">Key features</h2>
<ul class="dul">
<li><strong>Consistent backfills</strong> — Watermark-coordinated snapshots that merge seamlessly with the live WAL stream. Crash-resumable keyset cursors.</li>
<li><strong>Per-key ordering</strong> — Events for the same primary key always arrive in commit order. Configurable message grouping per table.</li>
<li><strong>Idempotency keys</strong> — Every event has a deterministic, stable key for exactly-once processing.</li>
<li><strong>Poison pill isolation</strong> — Failed events block only their message group, not the pipeline. Exponential backoff with dead-letter queue.</li>
<li><strong>High availability</strong> — Leader election via Postgres advisory locks. Automatic primary detection and failover.</li>
<li><strong>Multi-source</strong> — Capture from multiple databases in one process.</li>
<li><strong>Filtering</strong> — Table, operation, column, and SQL WHERE condition filters.</li>
</ul>`},

'docs-quickstart': {title:'Quick Start',sub:'Install kaptanto and stream your first events in under 2 minutes.',body:`
<h2 class="dh2">1. Install</h2>
<div class="dcode"><span class="tg">$</span> curl -fsSL https://get.kaptanto.dev | sh</div>
<p class="dp">Or with Docker:</p>
<div class="dcode"><span class="tg">$</span> docker pull kaptanto/kaptanto:latest</div>

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
<span class="tg">$</span> kaptanto --source postgres://... --output sse --port 7654

<span class="tc"># gRPC server</span>
<span class="tg">$</span> kaptanto --source postgres://... --output grpc --port 50051</div>
<p class="dp">Each connected client gets an independent consumer with its own cursor and checkpoint.</p>`},

'docs-install': {title:'Installation',sub:'Install kaptanto on Linux, macOS, or Windows.',body:`
<h2 class="dh2">Binary (recommended)</h2>
<div class="dcode"><span class="tg">$</span> curl -fsSL https://get.kaptanto.dev | sh</div>
<p class="dp">Downloads a statically-linked binary for your platform. No runtime dependencies.</p>

<h2 class="dh2">Docker</h2>
<div class="dcode"><span class="tg">$</span> docker pull kaptanto/kaptanto:latest
<span class="tg">$</span> docker run kaptanto/kaptanto --source postgres://host.docker.internal:5432/mydb --output stdout</div>

<h2 class="dh2">Homebrew</h2>
<div class="dcode"><span class="tg">$</span> brew install kaptanto/tap/kaptanto</div>

<h2 class="dh2">From source</h2>
<p class="dp">Requires Go 1.22+:</p>
<div class="dcode"><span class="tg">$</span> git clone https://github.com/kaptanto/kaptanto
<span class="tg">$</span> cd kaptanto && go build -o kaptanto ./cmd/kaptanto</div>

<h2 class="dh2">With Rust acceleration</h2>
<p class="dp">For maximum parsing performance, compile with the Rust FFI parser:</p>
<div class="dcode"><span class="tg">$</span> make build-rust</div>
<p class="dp">Requires Rust 1.77+ and CGO. Provides 30-40% lower CPU usage for the pgoutput decoding path.</p>`},

'docs-postgres': {title:'Connect Postgres',sub:'Configure Postgres for CDC with kaptanto.',body:`
<h2 class="dh2">Requirements</h2>
<ul class="dul">
<li><strong>Postgres 14+</strong> — Required for pgoutput logical decoding.</li>
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
    --collections page_views,user_sessions \\
    --output stdout</div>

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
  "table": "orders",
  "key": { "id": 1234 },
  "before": { "id": 1234, "status": "pending", "amount": 149.90 },
  "after":  { "id": 1234, "status": "settled", "amount": 149.90 },
  "metadata": {
    "lsn": "0/1A2B3C4",
    "tx_id": 84729,
    "checkpoint": "cGdfc2xvdF8x...",
    "snapshot": false
  }
}</div>

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
<table class="dtbl"><thead><tr><th>Strategy</th><th>Behavior</th></tr></thead><tbody>
<tr><td><code>snapshot_and_stream</code></td><td>Snapshot existing rows, then stream changes. Default.</td></tr>
<tr><td><code>stream_only</code></td><td>Skip snapshot. Only new changes.</td></tr>
<tr><td><code>snapshot_only</code></td><td>Snapshot then stop. One-time export.</td></tr>
<tr><td><code>snapshot_deferred</code></td><td>Stream immediately, snapshot on cron schedule.</td></tr>
<tr><td><code>snapshot_partial</code></td><td>Snapshot rows matching a WHERE condition.</td></tr>
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
<p class="dp">Every event is durably written to the embedded Event Log (Badger) before the source checkpoint is advanced. If kaptanto crashes between receiving a WAL message and writing it, the source re-sends on reconnection. The Event Log deduplicates by event ID.</p>

<h2 class="dh2">Poison pill handling</h2>
<p class="dp">Failed events are retried with exponential backoff (1s, 5s, 30s, 2min, 10min). After max retries, events move to a dead-letter partition. A failed event blocks only its own message group, not other groups in the same partition.</p>`},

'docs-ordering': {title:'Ordering and Partitions',sub:'How kaptanto maintains per-key order while maximizing throughput.',body:`
<h2 class="dh2">Message groups</h2>
<p class="dp">Events are partitioned by a configurable grouping key. By default, this is the primary key. All events for the same key land in the same partition and are delivered sequentially.</p>
<div class="dcode">tables:
  - name: orders
    group_by: [id]           # per-row ordering (default)
  - name: order_items
    group_by: [order_id]     # group by parent
  - name: metrics
    group_by: null           # no ordering, max throughput</div>

<h2 class="dh2">Partition isolation</h2>
<p class="dp">Each partition is served by a dedicated goroutine. If consumer A is slow on partition 7, partitions 0-6 and 8-63 continue at full speed for all consumers.</p>`},

'docs-stdout': {title:'stdout Output',sub:'The simplest output mode — pipe CDC events anywhere.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output stdout</div>
<p class="dp">One JSON line per event written to stdout. Pipe to jq, a subprocess, or any program that reads stdin.</p>

<h2 class="dh2">Filtering with jq</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output stdout | jq 'select(.operation == "insert")'</div>

<h2 class="dh2">Limitations</h2>
<p class="dp">Single consumer only. No acknowledgment — events are fire-and-forget. The consumer is responsible for its own checkpointing using the <code>metadata.checkpoint</code> field.</p>`},

'docs-sse': {title:'SSE Output',sub:'Server-Sent Events for multi-consumer HTTP streaming.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output sse --port 7654</div>

<h2 class="dh2">Connecting</h2>
<div class="dcode">GET http://localhost:7654/stream?tables=orders,payments&consumer=my-service</div>
<p class="dp">Each HTTP connection is an independent consumer with its own cursor. Supports <code>Last-Event-ID</code> header for automatic resume on reconnect.</p>

<h2 class="dh2">Event format</h2>
<div class="dcode">id: 01HX7K9M3N4P5Q6R7S8T9U0V
data: {"op":"update","table":"orders","after":{"id":1234,"status":"settled"}}

id: 01HX7K9M3N4P5Q6R7S8T9U0W
data: {"op":"insert","table":"payments","after":{"id":5678}}</div>`},

'docs-grpc': {title:'gRPC Output',sub:'High-performance streaming with Protocol Buffers.',body:`
<h2 class="dh2">Usage</h2>
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --output grpc --port 50051</div>

<h2 class="dh2">Proto definition</h2>
<div class="dcode">service CdcStream {
  rpc Subscribe(SubscribeRequest) returns (stream ChangeEvent);
  rpc Acknowledge(AckRequest) returns (AckResponse);
}</div>

<h2 class="dh2">Consumer pattern</h2>
<p class="dp">Call <code>Subscribe</code> to open a streaming connection. Call <code>Acknowledge</code> periodically with the checkpoint from the last processed event to advance your cursor.</p>
<p class="dp">HTTP/2 backpressure is native. If the consumer stops reading, the gRPC server detects window exhaustion and applies per-consumer backpressure.</p>`},

'docs-config': {title:'Configuration',sub:'CLI flags and YAML configuration reference.',body:`
<h2 class="dh2">CLI flags</h2>
<table class="dtbl"><thead><tr><th>Flag</th><th>Default</th><th>Description</th></tr></thead><tbody>
<tr><td><code>--source</code></td><td>required</td><td>Database connection string</td></tr>
<tr><td><code>--tables</code></td><td>required</td><td>Comma-separated table names</td></tr>
<tr><td><code>--output</code></td><td>stdout</td><td>Output mode: stdout, sse, grpc</td></tr>
<tr><td><code>--port</code></td><td>7654</td><td>Port for SSE/gRPC server</td></tr>
<tr><td><code>--config</code></td><td>-</td><td>Path to YAML config file</td></tr>
<tr><td><code>--data-dir</code></td><td>./data</td><td>Directory for Event Log and checkpoints</td></tr>
<tr><td><code>--retention</code></td><td>1h</td><td>Event Log retention period</td></tr>
<tr><td><code>--ha</code></td><td>false</td><td>Enable leader election</td></tr>
<tr><td><code>--node-id</code></td><td>auto</td><td>Unique node identifier for HA</td></tr>
</tbody></table>

<h2 class="dh2">YAML config</h2>
<div class="dcode">data_dir: /var/lib/kaptanto
retention: 4h
partitions: 64
ha:
  enabled: true
  node_id: node-1
sources:
  - id: main-pg
    type: postgres
    dsn: postgres://user:pass@host:5432/db
    tables:
      - name: orders
        snapshot: snapshot_and_stream
        group_by: [id]
        operations: [insert, update, delete]
      - name: users
        columns: [id, email, status]
        condition: "status != 'deleted'"
output:
  modes:
    - type: grpc
      port: 50051
metrics:
  enabled: true
  port: 9090</div>`},

'docs-filtering': {title:'Filtering',sub:'Control which events reach your consumers.',body:`
<h2 class="dh2">Table filtering</h2>
<p class="dp">Specify which tables to capture with <code>--tables</code> or in the YAML config.</p>

<h2 class="dh2">Operation filtering</h2>
<div class="dcode">tables:
  - name: audit_log
    operations: [insert]     # ignore updates and deletes</div>

<h2 class="dh2">Column filtering</h2>
<div class="dcode">tables:
  - name: users
    columns: [id, email, status, created_at]   # exclude PII columns</div>

<h2 class="dh2">Row filtering (WHERE condition)</h2>
<div class="dcode">tables:
  - name: orders
    condition: "status != 'draft' AND amount > 0"</div>
<p class="dp">For Postgres, row filters are applied at the publication level when possible, so filtered rows never leave the database.</p>`},

'docs-grouping': {title:'Message Grouping',sub:'Configure how events are partitioned for ordering.',body:`
<h2 class="dh2">Default: primary key</h2>
<p class="dp">By default, events are grouped by primary key. All events for the same row are delivered in order.</p>

<h2 class="dh2">Custom grouping</h2>
<div class="dcode">tables:
  - name: order_items
    group_by: [order_id]     # all items for same order are ordered
  - name: events
    group_by: [account_id]   # all events for same account
  - name: metrics
    group_by: null           # no ordering — maximum throughput</div>
<div class="dcall"><p><strong>Tradeoff:</strong> Coarse-grained grouping (e.g., account_id) reduces parallelism. Fine-grained grouping (e.g., primary key) maximizes throughput.</p></div>`},

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
<div class="dcode"><span class="tg">$</span> kaptanto --source postgres://... --metrics-port 9090
<span class="tc"># GET http://localhost:9090/metrics</span></div>

<h2 class="dh2">Key metrics</h2>
<table class="dtbl"><thead><tr><th>Metric</th><th>Type</th><th>Description</th></tr></thead><tbody>
<tr><td><code>kaptanto_source_lag_bytes</code></td><td>Gauge</td><td>WAL replication lag per source</td></tr>
<tr><td><code>kaptanto_events_captured_total</code></td><td>Counter</td><td>Events by source, table, operation</td></tr>
<tr><td><code>kaptanto_events_delivered_total</code></td><td>Counter</td><td>Events by consumer</td></tr>
<tr><td><code>kaptanto_consumer_lag_events</code></td><td>Gauge</td><td>Events behind per consumer</td></tr>
<tr><td><code>kaptanto_backfill_progress_pct</code></td><td>Gauge</td><td>Snapshot progress 0-100</td></tr>
<tr><td><code>kaptanto_errors_total</code></td><td>Counter</td><td>Errors by type</td></tr>
</tbody></table>

<h2 class="dh2">Health check</h2>
<div class="dcode">GET http://localhost:8080/healthz
<span class="tc"># 200 = healthy, 503 = unhealthy with diagnostic JSON</span></div>`},

'docs-api': {title:'Management API',sub:'REST API for programmatic control of kaptanto.',body:`
<h2 class="dh2">Endpoints</h2>
<table class="dtbl"><thead><tr><th>Method</th><th>Path</th><th>Description</th></tr></thead><tbody>
<tr><td><code>GET</code></td><td>/api/sources</td><td>List configured sources</td></tr>
<tr><td><code>GET</code></td><td>/api/sources/:id</td><td>Source details and status</td></tr>
<tr><td><code>POST</code></td><td>/api/sources/:id/tables</td><td>Add a table to capture</td></tr>
<tr><td><code>DELETE</code></td><td>/api/sources/:id/tables/:name</td><td>Remove a table</td></tr>
<tr><td><code>GET</code></td><td>/api/consumers</td><td>List active consumers</td></tr>
<tr><td><code>POST</code></td><td>/api/backfills</td><td>Trigger a backfill</td></tr>
<tr><td><code>GET</code></td><td>/api/backfills/:id</td><td>Backfill progress</td></tr>
</tbody></table>
<p class="dp">The management API is available when any output mode is running. It shares the same HTTP server as SSE and health endpoints.</p>`},

'docs-troubleshooting': {title:'Troubleshooting',sub:'Common issues and how to resolve them.',body:`
<h2 class="dh2">WAL bloat</h2>
<p class="dp">If kaptanto falls behind, Postgres retains WAL indefinitely. Monitor <code>kaptanto_source_lag_bytes</code> and set <code>wal_lag_alert_threshold</code> in your config.</p>

<h2 class="dh2">Slot does not exist</h2>
<p class="dp">After a failover, the replication slot may not exist on the new primary. Kaptanto detects this automatically, creates a new slot, and triggers a re-snapshot.</p>

<h2 class="dh2">TOAST column missing data</h2>
<p class="dp">Set <code>REPLICA IDENTITY FULL</code> on tables with large columns. Without it, unchanged TOAST columns appear as null in update events.</p>

<h2 class="dh2">Consumer falling behind</h2>
<p class="dp">Check <code>kaptanto_consumer_lag_events</code>. Configure <code>slow_consumer_policy</code> to <code>disconnect</code> if a consumer exceeds <code>max_lag_before_disconnect</code>.</p>`},

'docs-guides': {title:'Language Guides',sub:'Code examples for consuming kaptanto events in every language.',body:`
<div class="dcards">
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/python','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Python</h4></div><p>subprocess + stdout for ML fraud detection pipelines.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/node','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Node.js / TypeScript</h4></div><p>EventSource SSE for Elasticsearch index sync.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/go','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Go</h4></div><p>gRPC streaming for real-time balance materialization.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/java','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Java / Spring</h4></div><p>WebClient SSE for order notification services.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/rust','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Rust</h4></div><p>tonic gRPC for high-throughput analytics aggregation.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/ruby','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Ruby / Rails</h4></div><p>Open3 stdout for HIPAA audit trail logging.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/elixir','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>Elixir / Phoenix</h4></div><p>GenServer SSE for live dashboard via PubSub.</p></div>
<div class="dcard" onclick="window.open('https://github.com/kaptanto/kaptanto/tree/main/examples/dotnet','_blank')"><div class="lgc-h"><img src="/logo.png" alt="Kaptanto logo"><h4>C# / .NET</h4></div><p>BackgroundService gRPC for event sourcing projections.</p></div>
</div>
<p class="dp">All examples use the same kaptanto binary. The output mode (stdout, SSE, gRPC) determines how you connect. No SDK required.</p>
<h2 class="dh2">Deployment Guides</h2>
<div class="dcards">
<div class="dcard" onclick="go('docs-aws-setup')"><h4>AWS Deployment Guide</h4><p>Run kaptanto on ECS Fargate + RDS. Cost and infra compared to Debezium and Sequin.</p></div>
<div class="dcard" onclick="go('docs-benchmarks')"><h4>Benchmarks</h4><p>Throughput and latency results vs. Debezium and Sequin.</p></div>
</div>`},


'docs-aws-setup': {title:'AWS Deployment Guide',sub:'How to run kaptanto, Debezium, and Sequin alongside an API on AWS — and what each setup actually costs you.',body:`
<div class="dcall"><p><strong>Scenario:</strong> An order management API (Node.js / Python / any language) running on ECS Fargate, backed by RDS Postgres. Every row written via the API must be streamed to downstream consumers in real time.</p></div>

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
  "image": "kaptanto/kaptanto:latest",
  "command": [
    "--source", "postgres://api_user:pass@rds-host:5432/orders",
    "--tables", "orders,payments",
    "--output", "sse",
    "--port",   "7654",
    "--data-dir", "/data"
  ],
  "mountPoints": [{ "sourceVolume": "kaptanto-data", "containerPath": "/data" }]
}</div>
<p class="dp">Consumers anywhere in the VPC subscribe over HTTP. No broker, no queue, no extra AWS service:</p>
<div class="dcode"><span class="tc">// inventory-service.js — consuming kaptanto SSE</span>
import EventSource from <span class="ty">'eventsource'</span>;

const es = new EventSource(
  <span class="ty">'http://kaptanto.internal:7654/events?consumer=inventory'</span>
);

es.onmessage = (e) =&gt; {
  const evt = JSON.parse(e.data);
  if (evt.operation === <span class="ty">'insert'</span> &amp;&amp; evt.table === <span class="ty">'orders'</span>) {
    reserveInventory(evt.after.sku, evt.after.qty);
  }
};</div>
<p class="dp"><strong>AWS cost:</strong> ~$9/mo for a 0.25 vCPU / 0.5 GB Fargate task. EFS volume for the event log (~$0.30/GB/mo). Total overhead: roughly <strong>$10–15/mo</strong>.</p>
<div class="dcall"><p><strong>Failure model:</strong> If kaptanto restarts, it replays from its last checkpoint. The consumer reconnects with the same consumer ID and resumes from where it left off. No events lost.</p></div>

<h2 class="dh2">Option 2 — kaptanto-rust on ECS Fargate</h2>
<p class="dp">Identical setup to kaptanto. Change only the image tag:</p>
<div class="dcode">"image": "kaptanto/kaptanto:latest-rust"</div>
<p class="dp">Same cost, same operational model. The difference is behavioral: the Rust FFI WAL parser drains post-crash backlogs ~4x faster. Choose this over plain kaptanto when your consumers have SLAs on event freshness after a restart — for example, a financial reconciliation service that must be fully caught up within 60 seconds.</p>

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
<tr><td>Monthly overhead</td><td class="ck">~$15</td><td class="ck">~$15</td><td class="cx">$300–500+</td><td class="ca">$50–80</td></tr>
<tr><td>Consumer protocol</td><td>SSE / gRPC</td><td>SSE / gRPC</td><td>Kafka</td><td>HTTP push</td></tr>
<tr><td>Throughput ceiling</td><td>~36k eps</td><td>~32k eps</td><td>Kafka-bound</td><td class="cx">~500 eps</td></tr>
<tr><td>Post-crash drain (p50)</td><td>~30s</td><td class="ck">~8s</td><td class="cx">145s lag</td><td class="cx">172s lag</td></tr>
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
<li><strong>kaptanto</strong> — you're starting from scratch, want the lowest operational cost, and your consumers can hold a persistent HTTP connection. Covers 95% of use cases.</li>
<li><strong>kaptanto-rust</strong> — same as above, but your deployment restarts frequently (spot instances, rolling deploys) and you need sub-10s post-crash drain. The Rust build is worth the CI complexity.</li>
<li><strong>Debezium + MSK</strong> — your organization already pays for MSK and has Kafka-literate engineers. You need Kafka sink connectors (Snowflake, S3, BigQuery) or exactly-once semantics. Never set this up solely for CDC.</li>
<li><strong>Sequin</strong> — a product team that wants webhook-style delivery and will never exceed ~500 events/sec. The simplest possible consumer integration: just respond 200.</li>
</ul>
<div class="dcards">
<div class="dcard" onclick="go('docs-consistency')"><h4>Consistency Model</h4><p>Delivery guarantees, durability, and what happens on crash.</p></div>
<div class="dcard" onclick="go('docs-ha')"><h4>High Availability</h4><p>Leader election and multi-AZ failover.</p></div>
<div class="dcard" onclick="go('docs-guides')"><h4>Language Guides</h4><p>Full consumer examples in Node.js, Python, Go, and more.</p></div>
</div>`},

'docs-benchmarks': {title:'Benchmarks',sub:'Independent throughput and latency results vs. Debezium and Sequin.',body:`
<p class="dp">Tested on a single node running Postgres 16, 4 CDC scenarios, 2026-04-08. All tools consumed from the same database with equivalent workloads.</p>

<h2 class="dh2">Executive Summary</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Peak Throughput</th><th>p50 Latency</th><th>p95 Latency</th><th>Recovery</th><th>Infrastructure</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>36,267 eps</td><td>1,147 ms</td><td>16,864 ms</td><td>4.3 s</td><td>1 binary (Go, ~15 MB)</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>31,883 eps</td><td>993 ms</td><td>6,727 ms</td><td>3.1 s</td><td>1 binary (Go+Rust FFI, ~15 MB)</td></tr>
<tr><td>Debezium</td><td>351 eps</td><td>6,004 ms</td><td>7,371 ms</td><td>2.7 s</td><td>JVM + config files</td></tr>
<tr><td>Sequin</td><td>357 eps</td><td>1,579 ms</td><td>13,458 ms</td><td>81.8 s</td><td>Elixir + Redis + PG</td></tr>
</tbody>
</table></div>
<p class="dp">kaptanto delivers <strong>100&times; the peak throughput</strong> of Debezium and Sequin with no additional infrastructure.</p>

<h2 class="dh2">Throughput by Scenario (eps)</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady</th><th>Burst</th><th>Large Batch</th><th>Crash Recovery</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>4,805</td><td>7,141</td><td>36,267</td><td>2,594</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>3,559</td><td>6,061</td><td>31,883</td><td>1,394</td></tr>
<tr><td>Debezium</td><td>128</td><td>351</td><td>150</td><td>205</td></tr>
<tr><td>Sequin</td><td>220</td><td>357</td><td>324</td><td>86</td></tr>
</tbody>
</table></div>

<h2 class="dh2">Latency p50 / p95 / p99 (ms)</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady</th><th>Burst</th><th>Large Batch</th><th>Crash Recovery</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>1,147 / 16,864 / 19,997</td><td>2,858 / 9,823 / 11,658</td><td>2,656 / 6,953 / 7,391</td><td>29,851 / 124,989 / 140,213</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>993 / 6,727 / 10,062</td><td>4,563 / 12,520 / 14,177</td><td>2,731 / 6,929 / 7,373</td><td>7,590 / 34,166 / 39,436</td></tr>
<tr><td>Debezium</td><td>34,617 / 62,340 / 64,071</td><td>7,001 / 27,506 / 29,275</td><td>6,004 / 7,371 / 7,458</td><td>145,060 / 237,226 / 242,707</td></tr>
<tr><td>Sequin</td><td>23,638 / 60,133 / 62,574</td><td>1,579 / 13,458 / 14,338</td><td>5,034 / 7,305 / 7,464</td><td>172,153 / 242,202 / 245,573</td></tr>
</tbody>
</table></div>

<h2 class="dh2">RSS Memory Peak (MB)</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Steady</th><th>Burst</th><th>Large Batch</th><th>Crash Recovery</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>1,112</td><td>883</td><td>746</td><td>1,740</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>1,270</td><td>793</td><td>582</td><td>1,426</td></tr>
<tr><td>Debezium</td><td>365</td><td>360</td><td>273</td><td>469</td></tr>
<tr><td>Sequin</td><td>775</td><td>761</td><td>673</td><td>798</td></tr>
</tbody>
</table></div>

<h2 class="dh2">Crash Recovery Time</h2>
<div class="dtable-wrap"><table class="dtable">
<thead><tr><th>Tool</th><th>Recovery Time</th></tr></thead>
<tbody>
<tr><td><strong>kaptanto</strong></td><td>4.3 s</td></tr>
<tr><td><strong>kaptanto-rust</strong></td><td>3.1 s</td></tr>
<tr><td>Debezium</td><td>2.7 s</td></tr>
<tr><td>Sequin</td><td>81.8 s</td></tr>
</tbody>
</table></div>
<p class="dp">kaptanto and Debezium recover in seconds. Sequin requires 82 s to re-sync its internal state after a crash.</p>

<h2 class="dh2">Methodology</h2>
<p class="dp">Four scenarios were run sequentially against the same Postgres 16 instance:</p>
<ul class="dlist">
<li><strong>Steady</strong> — constant low-rate inserts</li>
<li><strong>Burst</strong> — spike of high-rate inserts followed by idle</li>
<li><strong>Large Batch</strong> — single bulk insert of 100k+ rows</li>
<li><strong>Crash Recovery</strong> — SIGKILL mid-stream, then restart and measure time to caught-up</li>
</ul>
<p class="dp">Throughput is measured as events-per-second at the consumer. Latency is end-to-end: row committed in Postgres to event received by consumer. Each tool ran in Docker with equivalent resource limits. Database state was reset between tools to eliminate cross-run contamination.</p>`}
};

// ── SIDEBAR ──
var sidebar = [
{label:'Get Started',items:[['docs-intro','Introduction'],['docs-quickstart','Quick Start'],['docs-install','Installation'],['docs-postgres','Connect Postgres'],['docs-mongo','Connect MongoDB']]},
{label:'Core Concepts',items:[['docs-schema','Event Schema'],['docs-backfills','Backfills'],['docs-consistency','Consistency Model'],['docs-ordering','Ordering & Partitions']]},
{label:'Output Modes',items:[['docs-stdout','stdout'],['docs-sse','Server-Sent Events'],['docs-grpc','gRPC']]},
{label:'Configuration',items:[['docs-config','CLI & YAML'],['docs-filtering','Filtering'],['docs-grouping','Message Grouping']]},
{label:'Production',items:[['docs-ha','High Availability'],['docs-metrics','Metrics & Monitoring'],['docs-api','Management API'],['docs-troubleshooting','Troubleshooting']]},
{label:'Guides',items:[['docs-guides','Language Guides'],['docs-aws-setup','AWS Deployment Guide'],['docs-benchmarks','Benchmarks']]}
];

function buildSidebar(active){
var h='';sidebar.forEach(function(s){
h+='<div class="dss"><div class="dsl">'+s.label+'</div>';
s.items.forEach(function(i){h+='<a class="dsa'+(i[0]===active?' act':'')+'" href="/docs/'+i[0]+'" onclick="return go(\''+i[0]+'\')">' +i[1]+'</a>'});
h+='</div>';
});
document.getElementById('docSidebar').innerHTML=h;
}

var docFlow=['docs-intro','docs-quickstart','docs-install','docs-postgres','docs-mongo','docs-schema','docs-backfills','docs-consistency','docs-ordering','docs-stdout','docs-sse','docs-grpc','docs-config','docs-filtering','docs-grouping','docs-ha','docs-metrics','docs-api','docs-troubleshooting','docs-guides','docs-aws-setup','docs-benchmarks'];

function docLabel(id){
for(var si=0;si<sidebar.length;si++){
for(var ii=0;ii<sidebar[si].items.length;ii++){
if(sidebar[si].items[ii][0]===id)return sidebar[si].items[ii][1];
}
}
return docs[id]?docs[id].title:id;
}

function renderNextSteps(id){
var i=docFlow.indexOf(id);
if(i===-1)return '';
var next1=docFlow[(i+1)%docFlow.length];
var next2=docFlow[(i+2)%docFlow.length];
var cards=[[next1,docLabel(next1),'Next page.'],[next2,docLabel(next2),'Then read this.']];
var h='<h2 class="dh2">Next steps</h2><div class="dcards">';
cards.forEach(function(c){
h+='<a class="dcard" href="/docs/'+c[0]+'" onclick="return go(\''+c[0]+'\')"><h4>'+c[1]+'</h4><p>'+c[2]+'</p></a>';
});
return h+'</div>';
}

function animateDynamicCards(){
document.querySelectorAll('.dcards .dcard').forEach(function(card,i){
card.style.setProperty('--stagger',((i%8)*70)+'ms');
card.classList.add('ani');
});
}

function renderDoc(id){
var d=docs[id];if(!d)return;
document.getElementById('docContent').innerHTML='<div class="dhead"><img src="/logo.png" alt="Kaptanto logo" class="dlg"><h1>'+d.title+'</h1></div><p class="dsub">'+d.sub+'</p>'+d.body+renderNextSteps(id);
animateDynamicCards();
buildSidebar(id);
}

// ── ROUTING ──
function go(p,noPush){
document.querySelectorAll('.pg').forEach(function(e){e.classList.remove('vis')});
document.querySelectorAll('.nl a[data-p]').forEach(function(a){a.classList.remove('a')});
if(p==='landing'){
document.getElementById('pg-landing').classList.add('vis');
document.querySelector('.nl a[data-p="landing"]').classList.add('a');
if(!noPush)window.history.pushState({},'', '/');
}else{
document.getElementById('pg-docs').classList.add('vis');
document.querySelector('.nl a[data-p="docs"]').classList.add('a');
renderDoc(p);
if(!noPush)window.history.pushState({},'', '/?doc='+p);
}
window.scrollTo(0,0);return false;
}

function routeFromUrl(){
var path=window.location.pathname||'/';
var qs=new URLSearchParams(window.location.search||'');
if(path.indexOf('/docs/')===0){
var id=path.split('/docs/')[1].replace(/\/$/,'');
if(docs[id])return id;
}
var qd=qs.get('doc');
if(qd&&docs[qd])return qd;
return 'landing';
}

// ── INIT ──
// Stream
!function(){var e=[['si','INSERT','orders #4821'],['su','UPDATE','users #119'],['si','INSERT','payments #7703'],['sd','DELETE','sessions #3321'],['su','UPDATE','orders #4822'],['si','INSERT','invoices #902'],['su','UPDATE','inventory #445'],['sd','DELETE','tokens #8812'],['si','INSERT','audit_log #15590'],['su','UPDATE','accounts #77'],['si','INSERT','transfers #3320'],['su','UPDATE','shipments #663']];var h='';for(var r=0;r<2;r++)e.forEach(function(v){h+='<span class="se"><span class="'+v[0]+'">'+v[1]+'</span>'+v[2]+'</span>'});document.getElementById('stk').innerHTML=h}();

// Tabs
document.querySelectorAll('.it').forEach(function(t){t.addEventListener('click',function(){document.querySelectorAll('.it').forEach(function(x){x.classList.remove('a')});document.querySelectorAll('.ic').forEach(function(c){c.style.display='none'});t.classList.add('a');document.getElementById('ic-'+t.dataset.t).style.display='block'})});

// Copy
function cpC(b){var t=b.parentElement.textContent.replace('copy','').replace(/\$ /g,'').trim();navigator.clipboard.writeText(t).then(function(){b.textContent='copied!';setTimeout(function(){b.textContent='copy'},1400)})}

// Glitch title
var gh=document.querySelector('.hw h1');
if(gh){
  var gt=gh.textContent.trim();
  var gl1=document.createElement('div');
  gl1.className='glitch-layer glitch-layer-1';
  gl1.textContent=gt;
  var gl2=document.createElement('div');
  gl2.className='glitch-layer glitch-layer-2';
  gl2.textContent=gt;
  gh.style.position='relative';
  gh.appendChild(gl1);
  gh.appendChild(gl2);
}

// Scroll reveal
var obs=new IntersectionObserver(function(e){e.forEach(function(x){if(x.isIntersecting){x.target.classList.add('v');obs.unobserve(x.target)}})},{threshold:.06});
document.querySelectorAll('.sr').forEach(function(el){obs.observe(el)});

var initialRoute=routeFromUrl();
if(initialRoute==='landing'){go('landing',true)}else{go(initialRoute,true)}
window.addEventListener('popstate',function(){
var p=routeFromUrl();
if(p==='landing'){go('landing',true)}else{go(p,true)}
});
