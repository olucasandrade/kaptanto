# Requirements: Kaptanto

**Defined:** 2026-03-07
**Core Value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## v1 Requirements

Requirements for v0.1.0 release. Each maps to roadmap phases.

### Source Connectors

- [x] **SRC-01**: Kaptanto connects to Postgres via logical replication using pgoutput plugin (Postgres 14-17)
- [x] **SRC-02**: Kaptanto auto-creates replication slot and publication for configured tables
- [x] **SRC-03**: Kaptanto responds to PrimaryKeepalive messages and sends periodic standby status updates
- [x] **SRC-04**: Kaptanto reconnects with configurable backoff on connection loss (2s initial, 60s max)
- [x] **SRC-05**: Kaptanto supports multi-host DSN for automatic primary detection after failover
- [x] **SRC-06**: Kaptanto detects missing replication slot after failover, creates new slot, and triggers re-snapshot
- [x] **SRC-07**: Kaptanto monitors WAL lag and emits warning when lag exceeds configurable threshold
- [x] **SRC-08**: Kaptanto validates REPLICA IDENTITY setting on connect and warns for tables using default
- [ ] **SRC-09**: Kaptanto connects to MongoDB via Change Streams on specific collections (MongoDB 4.2+)
- [ ] **SRC-10**: Kaptanto persists MongoDB resume tokens and resumes from last token on restart
- [ ] **SRC-11**: Kaptanto detects expired/invalid resume token and triggers automatic re-snapshot
- [ ] **SRC-12**: Kaptanto handles MongoDB replica set elections transparently via driver

### Parser

- [x] **PAR-01**: Kaptanto decodes pgoutput wire format (Relation, Insert, Update, Delete, Begin, Commit messages)
- [x] **PAR-02**: Kaptanto maintains a TOAST cache and merges unchanged markers with cached values for complete rows
- [x] **PAR-03**: Kaptanto detects schema evolution (new Relation messages) and updates the RelationCache
- [ ] **PAR-04**: Kaptanto normalizes MongoDB BSON documents into the unified ChangeEvent format
- [x] **PAR-05**: Kaptanto generates deterministic idempotency keys: source:schema.table:pk:op:position

### Event Log

- [x] **LOG-01**: Kaptanto durably writes every parsed event to an embedded Badger store before advancing source checkpoint
- [x] **LOG-02**: Events are partitioned by hash(grouping_key) % num_partitions (default 64)
- [x] **LOG-03**: Events are deduplicated by event ID on write (idempotent append)
- [x] **LOG-04**: Events automatically expire after configurable retention period (default 1 hour)

### Backfill Engine

- [x] **BKF-01**: Kaptanto snapshots existing table rows using keyset cursor pagination (never OFFSET)
- [x] **BKF-02**: Kaptanto coordinates snapshots with live WAL stream using watermark deduplication
- [x] **BKF-03**: Kaptanto persists backfill cursor position on every batch for crash recovery
- [x] **BKF-04**: Kaptanto dynamically adjusts batch size based on query duration (adaptive batch sizing)
- [x] **BKF-05**: Kaptanto supports all snapshot strategies: snapshot_and_stream, stream_only, snapshot_only, snapshot_deferred, snapshot_partial

### Router

- [x] **RTR-01**: Events are routed to partitions based on configurable grouping key (default: primary key)
- [x] **RTR-02**: Each partition is served by a dedicated goroutine delivering events sequentially
- [x] **RTR-03**: Each consumer has independent cursors per partition (consumer isolation)
- [x] **RTR-04**: Failed events block only their message group, not the entire partition (poison pill isolation)
- [x] **RTR-05**: Failed events are retried with exponential backoff and moved to dead-letter after max retries

### Output Servers

- [x] **OUT-01**: stdout output writes one NDJSON line per event
- [x] **OUT-02**: SSE server supports multiple independent consumer connections
- [x] **OUT-03**: SSE server supports Last-Event-ID header for automatic resume on reconnect
- [x] **OUT-04**: SSE server sends periodic ping comments to keep connections alive through proxies
- [x] **OUT-05**: SSE server supports configurable CORS origins
- [x] **OUT-06**: gRPC server implements Subscribe (server-streaming) and Acknowledge (unary) RPCs
- [x] **OUT-07**: gRPC server supports protobuf serialization with JSON fallback
- [x] **OUT-08**: gRPC server uses HTTP/2 native backpressure for flow control

### Checkpointing

- [x] **CHK-01**: Source checkpoint (Postgres LSN / MongoDB resume token) is NEVER advanced until event is durably written
- [x] **CHK-02**: Consumer cursors are flushed to checkpoint store every configurable interval (default 5s)
- [x] **CHK-03**: All state is flushed on graceful shutdown (SIGTERM/SIGINT)
- [x] **CHK-04**: SQLite checkpoint store for single-instance mode (pure Go, no CGO)
- [ ] **CHK-05**: Postgres checkpoint store for HA mode (shared state between instances)

### Configuration

- [x] **CFG-01**: Kaptanto accepts CLI flags: --source, --tables, --output, --port, --config, --data-dir, --retention, --ha, --node-id
- [x] **CFG-02**: Kaptanto parses YAML config file with multi-source, per-table settings, and output modes
- [x] **CFG-03**: Kaptanto supports table filtering (include specific tables)
- [x] **CFG-04**: Kaptanto supports operation filtering per table (insert, update, delete)
- [x] **CFG-05**: Kaptanto supports column filtering per table (include specific columns)
- [x] **CFG-06**: Kaptanto supports SQL WHERE condition filtering per table

### High Availability

- [ ] **HA-01**: Kaptanto supports leader election via Postgres advisory locks
- [ ] **HA-02**: Standby instance polls for lock availability and takes over when primary drops
- [ ] **HA-03**: Active leader loads last checkpoint from shared Postgres store on takeover

### Observability

- [x] **OBS-01**: Kaptanto exposes Prometheus metrics endpoint (lag, throughput, backfill progress, errors, consumer lag)
- [x] **OBS-02**: Kaptanto exposes /healthz endpoint returning 200 when healthy, 503 with diagnostic JSON when not
- [x] **OBS-03**: Kaptanto emits structured JSON logs with configurable level (debug, info, warn, error)

### Event Schema

- [x] **EVT-01**: All events follow unified JSON format with id, idempotency_key, timestamp, source, operation, table, key, before, after, metadata
- [x] **EVT-02**: Events use ULID for sortable, time-ordered, unique IDs
- [x] **EVT-03**: Snapshot reads have operation "read" with snapshot metadata (progress, snapshot_id)
- [x] **EVT-04**: Control events signal pipeline state changes (snapshot_complete, table_added, schema_change)

### Performance

- [ ] **PRF-01**: Rust FFI parser accelerates pgoutput decoding, TOAST cache, and JSON serialization behind build tag
- [x] **PRF-02**: Pure Go fallback parser compiles by default without CGO
- [ ] **PRF-03**: Makefile supports both Go-only and Go+Rust build targets

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Configuration

- **CFG-07**: SIGHUP hot-reload for adding/removing tables without restart
- **CFG-08**: Dynamic table addition via ALTER PUBLICATION

### Operations

- **OPS-01**: Management REST API (GET/POST sources, tables, consumers, backfills)
- **OPS-02**: Badger value log GC on periodic ticker for disk reclamation

### Distribution

- **DST-01**: Docker multi-stage build (Rust -> Go -> scratch)
- **DST-02**: Homebrew tap
- **DST-03**: curl installer script
- **DST-04**: GitHub Actions CI (test, lint, build, release)

## Out of Scope

| Feature | Reason |
|---------|--------|
| Managed sink delivery (webhook, SQS, Kafka, S3) | Reserved for Kaptanto Cloud (SaaS) |
| Web dashboard | CLI + REST API + Grafana is sufficient |
| Transform functions (JavaScript/SQL) | Transforms belong in the consumer |
| Built-in Kafka wire protocol | Too much protocol complexity for a focused binary |
| Long-term retention (30+ days) | Event log is a buffer, not a warehouse |
| MySQL connector | Future database source, not v1 |
| Wasm plugins | Premature extensibility |
| Consumer authentication (JWT, mTLS) | Evaluate during hardening; not v1 |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| EVT-01 | Phase 7.3 (gap closure) | Complete |
| EVT-02 | Phase 1 | Complete (01-01) |
| CFG-01 | Phase 7.5 (gap closure) | Pending |
| OBS-03 | Phase 1 | Complete (01-01) |
| PRF-02 | Phase 1 | Complete |
| SRC-01 | Phase 7.3 (gap closure) | Complete |
| SRC-02 | Phase 2 | Complete |
| SRC-03 | Phase 7.3 (gap closure) | Complete |
| SRC-04 | Phase 2 | Complete |
| SRC-05 | Phase 2 | Complete |
| SRC-06 | Phase 7.4 (gap closure) | Complete |
| SRC-07 | Phase 2 | Complete |
| SRC-08 | Phase 2 | Complete |
| PAR-01 | Phase 7.3 (gap closure) | Complete |
| PAR-02 | Phase 2 | Complete (02-02) |
| PAR-03 | Phase 2 | Complete (02-02) |
| PAR-05 | Phase 2 | Complete (02-02) |
| CHK-01 | Phase 7.3 (gap closure) | Complete |
| CHK-03 | Phase 2 | Complete |
| CHK-04 | Phase 2 | Complete |
| LOG-01 | Phase 7.3 (gap closure) | Complete |
| LOG-02 | Phase 3 | Complete |
| LOG-03 | Phase 3 | Complete |
| LOG-04 | Phase 3 | Complete |
| BKF-01 | Phase 7.4 (gap closure) | Complete |
| BKF-02 | Phase 7.4 (gap closure) | Complete |
| BKF-03 | Phase 7.4 (gap closure) | Complete |
| BKF-04 | Phase 7.4 (gap closure) | Complete |
| BKF-05 | Phase 7.4 (gap closure) | Complete |
| EVT-03 | Phase 7.4 (gap closure) | Complete |
| EVT-04 | Phase 7.4 (gap closure) | Complete |
| RTR-01 | Phase 5 | Complete |
| RTR-02 | Phase 5 | Complete |
| RTR-03 | Phase 5 | Complete |
| RTR-04 | Phase 5 | Complete |
| RTR-05 | Phase 5 | Complete |
| OUT-01 | Phase 5 | Complete |
| OUT-02 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-03 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-04 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-05 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-06 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-07 | Phase 6 (verified Phase 7.1) | Complete |
| OUT-08 | Phase 6 (verified Phase 7.1) | Complete |
| CHK-02 | Phase 7.1 | Complete |
| CFG-03 | Phase 6 (verified Phase 7.1) | Complete |
| CFG-04 | Phase 6 (verified Phase 7.1) | Complete |
| OBS-01 | Phase 7.5 (gap closure) | Pending |
| OBS-02 | Phase 7.5 (gap closure) | Pending |
| CFG-02 | Phase 7 | Complete |
| CFG-05 | Phase 7.2 | Complete |
| CFG-06 | Phase 7.2 | Complete |
| HA-01 | Phase 8 | Pending |
| HA-02 | Phase 8 | Pending |
| HA-03 | Phase 8 | Pending |
| CHK-05 | Phase 8 | Pending |
| SRC-09 | Phase 9 | Pending |
| SRC-10 | Phase 9 | Pending |
| SRC-11 | Phase 9 | Pending |
| SRC-12 | Phase 9 | Pending |
| PAR-04 | Phase 9 | Pending |
| PRF-01 | Phase 10 | Pending |
| PRF-03 | Phase 10 | Pending |

**Coverage:**
- v1 requirements: 57 total
- Mapped to phases: 57
- Unmapped: 0

---
*Requirements defined: 2026-03-07*
*Last updated: 2026-03-15 after v1.0 milestone audit — BKF-01..BKF-05, EVT-03, EVT-04, SRC-06 reset to Pending (gap closure Phase 7.4 assigned); OBS-01, OBS-02, CFG-01 reset to Pending (gap closure Phase 7.5 assigned)*
