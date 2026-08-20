**KAPTANTO**

"Who Captures" — Universal Database CDC

Complete Technical Specification & Implementation Plan

Version 1.0 — March 2026

Sources: Postgres (WAL) · MongoDB (Change Streams)

Outputs: stdout · SSE · gRPC

# Table of Contents

1. Executive Summary

2. Product Definition & Scope

3. Architecture Overview

4. Source Connectors

5. Parser Layer (Go + Rust FFI)

6. Event Log (Embedded Durable Store)

7. Backfill Engine

8. Partitioned Router

9. Output Servers

10. Checkpoint & State Management

11. High Availability

12. Configuration Reference

13. Event Schema

14. Metrics & Observability

15. Implementation Roadmap

16. Key Dependencies

# 1. Executive Summary

Kaptanto is an open-source, single Go binary for universal database Change Data Capture. It connects to Postgres (via WAL logical replication) and MongoDB (via Change Streams) and emits a unified event stream via stdout, SSE, or gRPC. It is language-agnostic — any developer in any stack can consume events without an SDK.

Kaptanto is designed for production from day one. The open-source binary handles database HA/failover, multi-source aggregation, consistent backfills with watermark coordination, per-key ordering, consumer isolation, and crash recovery. The managed SaaS (Kaptanto Cloud) adds managed sink delivery, web dashboard, transforms, long-term retention, team management, and SLA — operational convenience, not core reliability.

**Core differentiators:** Real-time streaming (sub-millisecond). No Kafka or JVM dependency. Multi-database (Postgres + MongoDB). Single binary distribution (zero dependencies). Per-key ordering with consumer isolation. Consistent backfills with watermark deduplication. Free, MIT licensed.

|  |
| --- |
| **PERFORMANCE**  Performance target: 500K+ events/sec (pure Go), 1.5M+ events/sec (with Rust-accelerated parser). Memory footprint under 100MB for typical workloads. |

# 2. Product Definition & Scope

## 2.1 Open-Source Binary (v1 Scope)

* **Sources:** Postgres 14-17 (WAL logical replication), MongoDB 4.0+ (Change Streams)
* **Outputs:** stdout (NDJSON), Server-Sent Events (HTTP SSE), gRPC streaming
* **Backfills:** Consistent snapshots with watermark coordination, keyset cursor pagination, partial backfills, crash-resumable
* **Ordering:** Per-key sequential delivery with configurable message grouping
* **HA:** Leader election via Postgres advisory locks, automatic failover detection, shared checkpoint store
* **Multi-source:** Multiple database sources in one process via config file
* **Filtering:** Table filtering, operation filtering, column filtering, SQL WHERE conditions
* **Observability:** Prometheus metrics endpoint, health check endpoint, structured logging
* **Config:** CLI flags, YAML config file, SIGHUP hot-reload, basic REST management API
* **Distribution:** Single static binary (Go), Docker image, Homebrew, curl installer

## 2.2 Cloud-Only Features (NOT in open-source)

* Managed sink delivery (webhook, SQS, Kafka, Elasticsearch, Redis, etc.) with retries and DLQ
* Web dashboard for pipeline monitoring and configuration
* Transform functions (JavaScript/SQL)
* Event routing to dynamic destinations
* Long-term retention (30+ days vs configurable in open-source)
* Multi-tenant isolation
* Team management, RBAC, API keys, audit logs
* SOC 2 compliance, VPC peering, dedicated infrastructure
* SLA with financial credits

# 3. Architecture Overview

## 3.1 System Diagram

We don't have the diagram yet, but the architecture is already here

## 3.2 Data Flow

1. **Source Connector** connects to database replication protocol, receives raw binary messages (WAL records / oplog events), handles keepalive and connection management.
2. **Parser** decodes binary wire format into structured ChangeEvent. Handles TOAST merging, schema evolution, type decoding. Optional Rust FFI for hot-path acceleration.
3. **Backfill Engine** coordinates snapshot reads with the live WAL/oplog stream. Uses watermarks to detect and drop stale reads. Persists cursor position for crash recovery.
4. **Event Log** durably writes every event to an embedded Badger store, partitioned by hash(grouping\_key). Source checkpoint is only advanced AFTER durable write.
5. **Partitioned Router** reads from the event log, fans out to registered consumers. Each partition is processed sequentially. Each consumer has independent cursors per partition.
6. **Output Servers** deliver events to external consumers via stdout pipe, HTTP SSE stream, or gRPC stream. Each consumer connection maps to a router consumer with independent state.

## 3.3 Critical Invariant

|  |
| --- |
| **INVARIANT**  The source replication checkpoint (Postgres LSN / MongoDB resume token) is NEVER advanced until the event is durably written to the Event Log. If kaptanto crashes between receiving a WAL message and writing it to the log, the source re-sends the message on reconnection. The Event Log deduplicates by event ID. |

# 4. Source Connectors

## 4.1 Source Interface

|  |
| --- |
| type Source interface {  Connect(ctx context.Context) error  StartReplication(ctx context.Context, out chan<- RawMessage) error  Snapshot(ctx context.Context, tables []SnapshotRequest, out chan<- RawMessage) error  Ack(position uint64) error  Close() error  }  type RawMessage struct {  Payload []byte  LSN uint64 // Source position (LSN or oplog timestamp)  Timestamp time.Time  Source string // "postgres", "mongodb"  Table string // For routing during snapshot coordination  Key []byte // Primary key bytes (for watermark lookups)  IsSnapshot bool // true if from backfill, false if from WAL/oplog  } |

Sources emit RawMessage with raw bytes. Parsing happens in the next layer. This keeps connectors thin — they manage connections, keepalive, and checkpointing only.

## 4.2 Postgres Connector

### 4.2.1 Connection & Failover

* **Multi-host DSN:** Supports libpq multi-host format: postgres://host1:5432,host2:5432/mydb?target\_session\_attrs=read-write
* **Primary detection:** Checks pg\_is\_in\_recovery() on each connection attempt. Only connects to the primary (not in recovery).
* **Slot existence check:** On connect, verifies the replication slot exists. If missing (failover scenario), creates a new slot and flags needsSnapshot=true.
* **Reconnection loop:** Infinite retry with configurable backoff (default: 2s initial, 60s max). Logs warnings during reconnection.
* **Keepalive:** Responds to PrimaryKeepalive messages. Sends periodic standby status to prevent WAL bloat on idle tables (configurable interval, default 10s).

### 4.2.2 WAL Consumption

* **Library:** jackc/pglogrepl — the reference Go implementation for Postgres logical replication.
* **Plugin:** pgoutput (built into Postgres 10+). No extensions required.
* **Publication:** Auto-creates a publication (kaptanto\_pub) covering the configured tables. Supports ALTER PUBLICATION for dynamic table addition.
* **Slot:** Auto-creates a logical replication slot (kaptanto\_<source\_id>). Slot name is configurable.
* **LSN tracking:** Tracks the latest received LSN but only advances the acknowledged LSN after the Event Log confirms durable write.

### 4.2.3 Postgres-Specific Challenges

* **WAL bloat:** If the consumer falls behind, Postgres retains WAL indefinitely. Kaptanto monitors replication lag (pg\_stat\_replication) and emits a metric + log warning when lag exceeds wal\_lag\_alert\_threshold (default 100MB).
* **REPLICA IDENTITY:** Requires REPLICA IDENTITY FULL for complete before/after values on updates. Kaptanto checks this on connect and warns if tables use the default (primary key only).
* **Long-running transactions:** A single long transaction can hold WAL retention. Kaptanto monitors the oldest active transaction and alerts when it exceeds a configurable age.

## 4.3 MongoDB Connector

### 4.3.1 Connection & Failover

* **Driver:** Official MongoDB Go driver (go.mongodb.org/mongo-driver). Supports replica sets and sharded clusters natively.
* **Failover:** The driver automatically follows replica set elections. Resume tokens survive elections — no special handling needed.
* **Read preference:** Configurable. Default: primary. Option: secondary (reduces load on primary for oplog reads).

### 4.3.2 Change Stream Consumption

* **Granularity:** Watches specific collections (not entire database) for targeted capture.
* **Full document:** Uses fullDocument: 'updateLookup' to include the complete document on updates (not just the delta).
* **Resume token:** Stored in the checkpoint store. On restart, the Change Stream resumes from the last token.
* **Oplog size:** If the resume token has expired (oplog was truncated during a long outage), kaptanto detects InvalidResumeToken and triggers a re-snapshot.
* **Pipeline filtering:** Supports MongoDB aggregation pipeline stages for server-side filtering ($match, $project).

# 5. Parser Layer (Go + Rust FFI)

## 5.1 Parser Interface

|  |
| --- |
| type Parser interface {  Parse(raw RawMessage) (\*ChangeEvent, error)  } |

The parser converts raw binary messages into structured ChangeEvent objects. For Postgres, this involves decoding pgoutput wire format. For MongoDB, this involves normalizing BSON documents.

## 5.2 pgoutput Decoder (Postgres)

Every WAL message is a binary blob with a single-byte message type identifier followed by type-specific data. The decoder handles:

* **Relation messages (R):** Describe table schemas. Cached in a RelationCache keyed by relation OID. Must be received before any data messages for that table.
* **Insert messages (I):** New tuple data. Decoded using the cached relation schema.
* **Update messages (U):** Old tuple (if REPLICA IDENTITY FULL) + new tuple. TOAST handling applies here.
* **Delete messages (D):** Old tuple (full row if REPLICA IDENTITY FULL, or just key columns).
* **Begin/Commit (B/C):** Transaction boundaries. Used for optional transaction grouping.
* **Type/Origin:** Custom type definitions and origin tracking. Passed through as metadata.

## 5.3 TOAST Handling

|  |
| --- |
| **CRITICAL: TOAST**  Postgres stores large column values (>8KB) in a separate TOAST table. When a row is updated, if the TOASTed column did NOT change, pgoutput sends an 'unchanged' marker instead of the value. The parser must maintain a cache of full-row states and merge unchanged markers with cached values to reconstruct the complete row. |

TOAST cache: HashMap<(relation\_id, primary\_key) → full\_row\_values>. Updated on every insert and update. Evicted when a delete is received. For Rust FFI, this cache lives in Rust's heap (no GC pressure).

## 5.4 Schema Evolution

When a table is ALTERed (add column, rename column, change type), Postgres sends a new Relation message before the next data message for that table. The parser detects this, updates the RelationCache, and logs the schema change. Events after the change include the new columns; events before don't. This is correct behavior — the event reflects the schema at the time of the change.

## 5.5 Rust FFI Architecture

Build-tag approach: the binary compiles with a pure-Go fallback decoder by default. When compiled with CGO\_ENABLED=1 and the 'rust' build tag, a Rust shared library handles pgoutput decoding, TOAST merging, and JSON serialization.

**Where Rust earns its keep:** pgoutput binary decoding (30-40% less CPU), TOAST cache management (no GC pressure from large HashMap), and JSON serialization via serde\_json (3-5x faster than encoding/json).

**What stays in Go:** Source connectors (I/O-bound, great library ecosystem), router (channel fan-out), output servers (net/http, gRPC), CLI, config parsing.

# 6. Event Log (Embedded Durable Store)

## 6.1 Purpose

The Event Log decouples source producers from consumer readers. Every parsed event is durably written before the source checkpoint is advanced. Consumers read from the log at their own pace with independent cursors. This enables: consumer isolation (slow consumers don't block others), replay (new consumers can read historical events within the retention window), crash recovery (events survive binary restarts), and backfill coordination (snapshot reads and WAL events merge in the log).

## 6.2 Implementation: Badger

* **Engine:** Badger v4 (dgraph-io/badger). Pure Go, embedded, LSM tree. Optimized for write-heavy append workloads.
* **TTL:** Native support. Events automatically expire after the configured retention period (default: 1 hour).
* **Key format:** p:<partition\_id>:s:<sequence\_number> — enables efficient per-partition range scans.
* **Partitioning:** Events are assigned to partitions by hash(grouping\_key) % num\_partitions. Default: 64 partitions.
* **Deduplication:** Events include a deterministic ID. On write, if the ID already exists in the log, the write is skipped (idempotent append).

## 6.3 Event Log Interface

|  |
| --- |
| type EventLog interface {  Append(event \*ChangeEvent) (uint64, error) // Returns sequence number  ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]LogEntry, error)  Oldest() uint64 // Oldest available sequence  Latest() uint64 // Newest sequence  Close() error  } |

# 7. Backfill Engine

|  |
| --- |
| **COMPLEXITY WARNING**  The backfill engine is the most complex subsystem. Getting it wrong means delivering stale data, missing events, or duplicating rows. This design is informed by Sequin's watermark coordination approach. |

## 7.1 Backfill Strategies

|  |  |  |
| --- | --- | --- |
| **Strategy** | **Behavior** | **Use Case** |
| snapshot\_and\_stream | Snapshot existing rows, then stream WAL changes. Watermark coordination prevents stale reads. | Default. Most common use case. |
| stream\_only | Skip snapshot entirely. Only capture changes from now. | Notification services, append-only tables. |
| snapshot\_only | Snapshot existing rows, then stop. No streaming. | One-time data export, backfill tasks. |
| snapshot\_deferred | Start streaming immediately, schedule snapshot for later (cron). | Large tables where snapshot during peak hours is risky. |
| snapshot\_partial | Snapshot only rows matching a WHERE condition. | Disaster recovery: replay last 24 hours. |

## 7.2 Watermark Coordination

The core problem: while kaptanto is snapshotting a table (reading existing rows), the WAL stream continues producing events for the same table. A row might be read during the snapshot AND then updated in the WAL. Without coordination, the consumer receives both the stale snapshot read and the newer WAL update — delivering out-of-order or duplicate data.

**Solution:** Watermark-based deduplication. For every row read during a snapshot, kaptanto checks if the Event Log already contains a WAL event for the same primary key with a higher LSN. If so, the snapshot read is dropped — the WAL event is newer and takes precedence.

### 7.2.1 Watermark Flow

1. Start logical replication and begin buffering WAL events into the Event Log.
2. Note the starting LSN as snapshot\_lsn.
3. Begin snapshot using keyset cursor pagination.
4. For each batch of rows read from the snapshot:

* For each row, compute its primary key.
* Check the Event Log: does any WAL event exist for this (table, primary\_key) with LSN > snapshot\_lsn?
* If YES: drop the snapshot read (WAL event is newer).
* If NO: emit the snapshot read as a 'read' event into the Event Log.

1. When the snapshot completes, emit a 'snapshot\_complete' control event.
2. Continue streaming WAL events normally.

## 7.3 Keyset Cursor Pagination

Kaptanto uses keyset cursors (not OFFSET) for snapshot pagination. OFFSET-based pagination breaks when rows are inserted or deleted during the backfill — rows get skipped or duplicated.

|  |
| --- |
| -- First batch  SELECT \* FROM orders ORDER BY id ASC LIMIT 5000;  -- Subsequent batches (keyset cursor)  SELECT \* FROM orders WHERE id > $1 ORDER BY id ASC LIMIT 5000;  -- $1 = last id from previous batch |

For partial backfills with a custom sort column:

|  |
| --- |
| -- Partial backfill: orders from last 30 days, sorted by updated\_at  SELECT \* FROM orders  WHERE updated\_at >= '2026-02-04'  AND (updated\_at, id) > ($1, $2)  ORDER BY updated\_at ASC, id ASC  LIMIT 5000; |

## 7.4 Adaptive Batch Sizing

Kaptanto dynamically adjusts batch sizes during backfills to maximize throughput without overloading the source database. A BatchOptimizer starts with a default batch size (5000 rows), measures query duration, and adjusts:

* If query < 1s: increase batch size by 25% (up to max 50,000)
* If query > 3s: decrease batch size by 50%
* If query > query\_timeout (default 5s): halve batch size aggressively
* Minimum batch size: 100 rows

## 7.5 Crash-Resumable Backfills

Backfill cursor position is persisted to the checkpoint store on every batch. If kaptanto crashes during a 5-million-row backfill, it resumes from the last persisted cursor — not from zero.

|  |
| --- |
| type BackfillState struct {  SourceID string  Table string  Status string // running, paused, completed, failed  Strategy string  CursorKey []byte // Last primary key processed  CursorSort any // Last sort column value (for partial backfills)  TotalRows int64 // Estimated total (from pg\_class.reltuples)  ProcessedRows int64  SnapshotLSN uint64 // LSN at snapshot start (for watermark coord)  StartedAt time.Time  UpdatedAt time.Time  } |

# 8. Partitioned Router

## 8.1 Message Grouping

Events are assigned to partitions based on a configurable grouping key. By default, the grouping key is the primary key — all events for the same row land in the same partition and are delivered sequentially. Users can configure custom grouping keys per table:

|  |
| --- |
| tables:  - name: orders  group\_by: [id] # Default: primary key. Per-row ordering.  - name: order\_items  group\_by: [order\_id] # Group by parent. All items for same order are ordered.  - name: events  group\_by: [account\_id] # Group by account. All events for same account are ordered.  - name: metrics  group\_by: null # No grouping. Maximum throughput, no ordering guarantees. |

## 8.2 Per-Partition Sequential Delivery

Each partition is served by a dedicated goroutine that reads from the Event Log sequentially. Events for partition N are delivered to all consumers subscribed to that partition in order. Different partitions execute concurrently.

If consumer A is slow processing events from partition 7, only partition 7's reader blocks waiting for consumer A's channel. Partitions 0-6 and 8-63 continue delivering to all consumers at full speed.

## 8.3 Poison Pill Isolation

When a consumer fails to process an event (gRPC error, SSE connection drop, stdout pipe broken), the event is NOT retried indefinitely in-line. Instead:

1. The event is marked as 'failed' with an attempt count and next-retry timestamp.
2. The partition reader advances past it to continue delivering subsequent events to other consumers.
3. A separate retry goroutine attempts redelivery with exponential backoff (1s, 5s, 30s, 2min, 10min).
4. After max retries (configurable, default 15), the event is moved to a dead-letter partition.
5. Subsequent events for the SAME grouping key are blocked until the failed event is resolved (prevents out-of-order delivery).
6. Events for OTHER grouping keys in the same partition continue flowing.

|  |
| --- |
| **DESIGN NOTE**  This matches Sequin's behavior: a failed message blocks only its own message group, not the entire pipeline. The dead-letter partition is queryable via the management API. |

# 9. Output Servers

## 9.1 stdout

One JSON line per event written to stdout. The simplest output — pipe to jq, a subprocess, or /dev/null for benchmarking. Single-consumer only. No acknowledgment — events are fire-and-forget. Consumer is responsible for its own checkpointing via the checkpoint field in each event.

## 9.2 Server-Sent Events (SSE)

* **Endpoint:** GET /stream?tables=orders,payments&consumer=my-service
* **Multi-connection:** Each HTTP connection is an independent consumer with its own cursor in the Event Log.
* **Auto-reconnect:** Supports Last-Event-ID header. On reconnect, the consumer resumes from its last received event ID.
* **Ping:** SSE comment lines sent at configurable intervals (default 15s) to keep connections alive through proxies.
* **CORS:** Configurable CORS origins for browser-based consumers.
* **Backpressure:** If the client isn't reading fast enough, the HTTP write buffer fills. The router detects this and applies per-consumer backpressure.

## 9.3 gRPC

* **Service:** CdcStream with Subscribe (server-streaming) and Acknowledge (unary) RPCs.
* **Multi-consumer:** Each Subscribe call creates an independent consumer.
* **Checkpointing:** Consumers call Acknowledge with their consumer\_id and the checkpoint from the last processed event. This advances the consumer's cursor.
* **Flow control:** HTTP/2 backpressure is native. If the consumer stops reading from the stream, the gRPC server detects window exhaustion.
* **Protobuf:** Events serialized as protobuf for maximum throughput. JSON fallback available via content-type negotiation.

# 10. Checkpoint & State Management

## 10.1 What Is Checkpointed

* **Source position:** Postgres LSN, MongoDB resume token. Advanced only after Event Log durable write.
* **Consumer cursors:** Per-consumer, per-partition sequence numbers in the Event Log.
* **Backfill state:** Cursor position, progress counters, snapshot LSN.
* **Failed events:** Retry count, next attempt time, error message.

## 10.2 Storage Backends

|  |  |  |
| --- | --- | --- |
| **Backend** | **When Used** | **Details** |
| SQLite (embedded) | Single-instance, no HA | Default. modernc.org/sqlite (pure Go, no CGO). File in data\_dir. Zero external deps. |
| Postgres (shared) | HA mode, multi-instance | Stores checkpoints in a kaptanto schema on the source Postgres. Enables standby to resume from primary's position. |

## 10.3 Flush Frequency

Consumer cursors are flushed to the checkpoint store every checkpoint\_interval (default: 5 seconds). Source positions are flushed on every batch acknowledgment. Backfill cursors are flushed on every batch completion. On graceful shutdown (SIGTERM/SIGINT), all state is flushed before exit.

# 11. High Availability

## 11.1 Database Failover (Postgres)

Kaptanto handles primary failover by: detecting connection loss, reconnecting with multi-host DSN and target\_session\_attrs=read-write, checking if the replication slot exists on the new primary, creating a new slot + triggering re-snapshot if needed, and resuming from the last acknowledged LSN.

## 11.2 Database Failover (MongoDB)

Handled natively by the driver. Replica set elections trigger automatic reconnection. Resume tokens survive elections. If the oplog is truncated during a long outage, kaptanto detects InvalidResumeToken and triggers re-snapshot.

## 11.3 Kaptanto Agent Failover

Two kaptanto instances run against the same source database. Only one actively consumes. Leader election via Postgres advisory locks:

1. Both instances start and attempt pg\_try\_advisory\_lock(lock\_id) on the source database.
2. The instance that acquires the lock starts consuming. The other loops, retrying every 5 seconds.
3. If the primary crashes, its TCP connection drops, the advisory lock is released automatically by Postgres.
4. The standby acquires the lock, loads the last checkpoint from the shared Postgres checkpoint store, and resumes.
5. Failover time: ~5-10 seconds (lock poll interval + connection setup).

|  |
| --- |
| **WHY ADVISORY LOCKS**  Advisory locks are session-scoped. No TTL, no clock skew, no split-brain risk from expiration race conditions. The lock is released the instant the TCP connection closes. |

# 12. Configuration Reference

See the companion kaptanto-config-reference.yaml for the complete annotated configuration file. Key sections:

* **Global:** data\_dir, retention, partitions, log\_level, log\_format
* **HA:** enabled, node\_id, lock\_backend, heartbeat\_interval
* **Sources:** id, type, dsn, slot\_name, publication, tables (with per-table snapshot strategy, grouping, filtering, columns)
* **Output:** modes (stdout, sse, grpc), each with host, port, connection limits
* **Events:** id\_format, timestamp\_format, include\_before, include\_transaction
* **Consumers:** default\_buffer\_size, checkpoint\_interval, slow\_consumer\_policy, max\_lag\_before\_disconnect
* **Metrics:** enabled, port, path, format
* **Health:** enabled, port, path, thresholds

## 12.1 Minimal Config

|  |
| --- |
| kaptanto --source postgres://localhost:5432/mydb --tables orders --output stdout |

All defaults apply: snapshot\_and\_stream, 1hr retention, 64 partitions, no HA, no metrics.

## 12.2 Production Config (YAML)

|  |
| --- |
| data\_dir: /var/lib/kaptanto  retention: 4h  partitions: 64  ha:  enabled: true  node\_id: node-1  sources:  - id: main-pg  type: postgres  dsn: postgres://user:pass@primary:5432,standby:5432/prod?target\_session\_attrs=read-write  tables:  - name: orders  snapshot: snapshot\_and\_stream  group\_by: [id]  operations: [insert, update, delete]  - name: users  snapshot: snapshot\_and\_stream  columns: [id, email, status, created\_at]  condition: "status != 'deleted'"  output:  modes:  - type: grpc  port: 50051  - type: sse  port: 7654  metrics:  enabled: true  port: 9090  health:  enabled: true  port: 8080 |

# 13. Event Schema

## 13.1 Standard CDC Event

|  |
| --- |
| {  "id": "01HX7K9M3N4P5Q6R7S8T9U0V",  "idempotency\_key": "main-pg:public.orders:1234:update:0/1A2B3C4",  "timestamp": "2026-03-06T14:32:01.847Z",  "source": "main-pg",  "operation": "update",  "database": "production",  "schema": "public",  "table": "orders",  "key": { "id": 1234 },  "before": { "id": 1234, "status": "pending", "amount": 149.90 },  "after": { "id": 1234, "status": "settled", "amount": 149.90 },  "metadata": {  "lsn": "0/1A2B3C4",  "tx\_id": 84729,  "commit\_time": "2026-03-06T14:32:01.845Z",  "checkpoint": "cGdfc2xvdF8x...",  "snapshot": false  }  } |

## 13.2 Idempotency Key

|  |
| --- |
| **KEY DESIGN**  Every event has a deterministic idempotency\_key: source\_id:schema.table:primary\_key:operation:position. This key is stable across restarts — if kaptanto replays events, the same event produces the same key. Consumers use this for deduplication and exactly-once processing. |

## 13.3 Snapshot Read Event

|  |
| --- |
| {  "id": "01HX7K9M3N4P5Q6R7S8T9U0W",  "idempotency\_key": "main-pg:public.users:42:read:snap\_01HX7K",  "timestamp": "2026-03-06T14:32:01.847Z",  "source": "main-pg",  "operation": "read",  "table": "users",  "key": { "id": 42 },  "before": null,  "after": { "id": 42, "email": "user@example.com", "name": "Lucas" },  "metadata": {  "checkpoint": "c25hcHNob3RfMQ==",  "snapshot": true,  "snapshot\_id": "snap\_01HX7K",  "snapshot\_progress": { "total": 5000000, "completed": 1250000, "pct": 25.0 }  }  } |

## 13.4 Control Events

Control events signal pipeline state changes. They have operation: 'control' and a control\_type field:

* **snapshot\_complete:** Emitted when a backfill finishes. Includes total rows, duration, and the transition checkpoint.
* **table\_added:** Emitted when a table is dynamically added to capture.
* **table\_removed:** Emitted when a table is removed from capture.
* **schema\_change:** Emitted when a table schema change (ALTER TABLE) is detected.

# 14. Metrics & Observability

## 14.1 Prometheus Metrics

|  |  |  |
| --- | --- | --- |
| **Metric** | **Type** | **Description** |
| kaptanto\_source\_lag\_bytes | Gauge | WAL/oplog replication lag in bytes per source |
| kaptanto\_source\_lag\_seconds | Gauge | Estimated seconds behind real-time per source |
| kaptanto\_events\_captured\_total | Counter | Events captured, labeled by source, table, operation |
| kaptanto\_events\_delivered\_total | Counter | Events delivered, labeled by consumer, table, operation |
| kaptanto\_consumer\_lag\_events | Gauge | Events behind per consumer |
| kaptanto\_backfill\_progress\_pct | Gauge | Snapshot progress per table (0-100) |
| kaptanto\_backfill\_rows\_per\_sec | Gauge | Current backfill throughput per table |
| kaptanto\_event\_log\_size\_bytes | Gauge | Size of the embedded event log on disk |
| kaptanto\_parser\_duration\_seconds | Histogram | Time to parse a raw message (Go vs Rust) |
| kaptanto\_checkpoint\_flushes\_total | Counter | Checkpoint store write operations |
| kaptanto\_errors\_total | Counter | Errors labeled by type (parse, deliver, connect, checkpoint) |
| kaptanto\_failed\_events\_total | Counter | Events that failed delivery and entered retry/DLQ |
| kaptanto\_ha\_leader | Gauge | 1 if this instance is the active leader, 0 if standby |

## 14.2 Health Endpoint

GET /healthz returns 200 when all sources are connected, WAL lag is below threshold, and Event Log is writable. Returns 503 with diagnostic JSON when unhealthy.

## 14.3 Structured Logging

JSON-formatted logs (configurable: json or text) with structured fields: level, msg, source\_id, table, consumer\_id, lsn, duration, error. Log levels: debug, info, warn, error.

# 15. Implementation Roadmap

## Phase 1: Foundation (Weeks 1-4)

Goal: Single Postgres source → stdout output. Core pipeline functional.

* CLI entrypoint with cobra (--source, --tables, --output flags)
* Postgres connector: connect, create slot/publication, WAL consumption loop with pglogrepl
* pgoutput decoder (pure Go): Relation, Insert, Update, Delete message parsing
* TOAST cache and merging
* ChangeEvent struct with JSON serialization
* stdout output writer (NDJSON)
* LSN checkpointing to SQLite
* Heartbeat mechanism (standby status updates)
* Basic structured logging
* **Deliverable:** kaptanto --source postgres://... --tables orders --output stdout works end-to-end

## Phase 2: Event Log & Backfills (Weeks 5-8)

Goal: Durable event log, consistent backfills, crash recovery.

* Badger-based Event Log with partitioned append
* Event deduplication by idempotency key
* Deterministic idempotency key generation
* Keyset cursor pagination for snapshots
* Watermark coordinator (snapshot-vs-WAL deduplication)
* Backfill state persistence and crash recovery
* Adaptive batch sizing (BatchOptimizer)
* Snapshot strategies: snapshot\_and\_stream, stream\_only
* 'read' operation type for snapshot events
* 'control' events: snapshot\_complete
* **Deliverable:** Connect to a table with existing rows, receive complete dataset + streaming changes

## Phase 3: Router & Multi-Consumer (Weeks 9-12)

Goal: Partitioned routing, SSE + gRPC outputs, multiple consumers.

* Partitioned router with configurable message grouping
* Per-consumer independent cursors
* Per-partition backpressure isolation
* Poison pill isolation with retry and DLQ
* SSE output server (multi-connection, Last-Event-ID, CORS, ping)
* gRPC output server (Subscribe streaming, Acknowledge, protobuf)
* Consumer registration/deregistration
* Slow consumer detection and configurable policy (block/drop/disconnect)
* **Deliverable:** Multiple gRPC/SSE consumers with per-key ordering and independent progress

## Phase 4: Multi-Source, Filtering, Config (Weeks 13-16)

Goal: Multiple sources in one process, filtering, full YAML config.

* Source Manager: multiple sources with independent lifecycles and auto-restart
* YAML config file parsing and validation
* SIGHUP config hot-reload (add/remove tables without restart)
* Dynamic table addition via ALTER PUBLICATION
* Table filtering, operation filtering, column filtering
* SQL WHERE condition filtering (evaluated in Go)
* Partial backfills (snapshot\_where, sort column selection)
* Deferred backfills (snapshot\_deferred with cron schedule)
* Transaction grouping (optional begin/commit events)
* **Deliverable:** Multi-source YAML config with filtered, ordered CDC pipeline

## Phase 5: HA & Observability (Weeks 17-20)

Goal: Production-grade HA, metrics, health checks, management API.

* Postgres advisory lock-based leader election
* Shared checkpoint store (Postgres backend)
* Standby instance loop with automatic takeover
* Prometheus metrics endpoint (all metrics from section 14)
* Health check endpoint (/healthz)
* Basic REST management API (GET/POST sources, tables, consumers, backfills)
* WAL bloat monitoring and alerting (wal\_lag\_alert\_threshold)
* REPLICA IDENTITY validation on connect
* **Deliverable:** Two kaptanto instances with automatic failover. Prometheus + health endpoint.

## Phase 6: MongoDB & Polish (Weeks 21-24)

Goal: MongoDB source, Rust FFI, performance tuning, documentation, release.

* MongoDB connector: Change Streams, resume tokens, failover
* BSON normalizer (MongoDB document → flat ChangeEvent)
* MongoDB snapshot via Find with resume token bookmarking
* Rust FFI: pgoutput decoder, TOAST cache, JSON serialization
* Build system: Makefile with Go-only and Go+Rust targets
* Docker multi-stage build (Rust → Go → scratch)
* Performance benchmarking suite
* Comprehensive documentation (README, config reference, architecture guide)
* Homebrew tap, curl installer script
* GitHub Actions CI (test, lint, build, release)
* **Deliverable:** v0.1.0 release. Postgres + MongoDB. All output modes. HA. Metrics. Docs.

# 16. Key Dependencies

## 16.1 Go

|  |  |  |
| --- | --- | --- |
| **Package** | **Purpose** | **Notes** |
| jackc/pglogrepl | Postgres logical replication | Reference Go impl. By the pgx author. |
| jackc/pgx/v5 | Postgres driver (snapshots) | Used for COPY, queries, advisory locks. |
| go.mongodb.org/mongo-driver | MongoDB driver + Change Streams | Official driver. Full replica set support. |
| dgraph-io/badger/v4 | Embedded Event Log | LSM tree, TTL, pure Go. |
| google.golang.org/grpc | gRPC server | Reference Go gRPC implementation. |
| google.golang.org/protobuf | Protobuf serialization | Code generation for gRPC. |
| modernc.org/sqlite | Checkpoint store (local) | Pure Go SQLite. No CGO. |
| spf13/cobra | CLI framework | Standard Go CLI library. |
| golang.org/x/sync | errgroup | Goroutine lifecycle management. |
| prometheus/client\_golang | Prometheus metrics | Standard Go Prometheus client. |
| oklog/ulid | Event IDs | Sortable, unique, time-ordered. |

## 16.2 Rust (Optional FFI)

|  |  |  |
| --- | --- | --- |
| **Crate** | **Purpose** | **Notes** |
| serde + serde\_json | JSON serialization | 3-5x faster than encoding/json. |
| fnv | Hash function | Fast non-crypto hash for partitioning. |

End of Specification

kaptanto — who captures (Esperanto)