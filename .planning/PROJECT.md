# Kaptanto

## What This Is

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It connects to Postgres (via WAL logical replication) and MongoDB (via Change Streams) and emits a unified event stream via stdout, SSE, or gRPC. Language-agnostic — any developer in any stack can consume events without an SDK.

## Core Value

Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Postgres WAL consumption with pgoutput decoding, TOAST handling, and schema evolution
- [ ] Embedded durable Event Log (Badger) with partitioned append, TTL, and deduplication
- [ ] Consistent backfills with watermark coordination, keyset cursors, crash recovery
- [ ] Partitioned router with per-key ordering, consumer isolation, and poison pill handling
- [ ] stdout, SSE, and gRPC output servers with independent consumers
- [ ] Multi-source aggregation with YAML config, hot-reload, and filtering
- [ ] HA via Postgres advisory lock leader election with shared checkpoint store
- [ ] Prometheus metrics, health endpoint, and management REST API
- [ ] MongoDB Change Streams connector with resume tokens and re-snapshot detection
- [ ] Optional Rust FFI for pgoutput decoding, TOAST cache, and JSON serialization

### Out of Scope

- Landing page / marketing site — already built, maintained separately
- Kaptanto Cloud (managed sinks, dashboard, transforms, RBAC, multi-tenant) — SaaS product, not open-source binary
- MySQL connector — future database source, not v1

## Context

The full technical specification exists at `kaptanto-technical-specification.md` and is the authoritative source of truth for architecture, interfaces, data flow, event schema, and configuration. The spec includes Go interfaces for Source, Parser, EventLog, and detailed protocol-level documentation for pgoutput decoding and MongoDB Change Streams.

Key Go packages: jackc/pglogrepl (WAL), jackc/pgx/v5 (Postgres driver), dgraph-io/badger/v4 (Event Log), modernc.org/sqlite (checkpoint store), spf13/cobra (CLI), google.golang.org/grpc, prometheus/client_golang, oklog/ulid.

Performance targets: 500K+ events/sec pure Go, 1.5M+ with Rust FFI. Memory under 100MB for typical workloads.

## Constraints

- **Distribution**: Single static binary, zero external dependencies (no Kafka, no JVM)
- **Go version**: 1.22+ required
- **Postgres**: 14-17 with wal_level=logical
- **MongoDB**: 4.0+ replica set or sharded cluster
- **Rust FFI**: Optional, behind build tag (CGO_ENABLED=1 + 'rust' build tag)
- **Checkpoint invariant**: Source LSN/resume token NEVER advanced until event is durably written to Event Log

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Badger v4 for Event Log | Pure Go, LSM tree optimized for write-heavy append, native TTL | — Pending |
| pgoutput (not wal2json) | Built into Postgres 10+, no extensions required | — Pending |
| Advisory locks for HA | Session-scoped, no TTL/clock skew, released on TCP close | — Pending |
| modernc.org/sqlite for checkpoints | Pure Go SQLite, no CGO dependency for default build | — Pending |
| Keyset cursors for snapshots | OFFSET breaks on concurrent inserts/deletes | — Pending |
| Watermark dedup for backfills | Prevents stale reads during snapshot+WAL coordination | — Pending |

---
*Last updated: 2026-03-07 after initialization*
