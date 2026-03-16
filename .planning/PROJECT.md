# Kaptanto

## What This Is

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It connects to Postgres (via WAL logical replication) and streams a unified JSON event feed via stdout, SSE, or gRPC — with a durable embedded event log, consistent backfills, and Prometheus observability. Language-agnostic: any stack can consume events without an SDK.

**v1.0 shipped:** Full Postgres CDC pipeline — WAL decoding, durable Event Log, backfill engine, partitioned router, three output modes, YAML config, and Prometheus metrics. 10,749 LOC Go, 114 commits, 9 days.

## Core Value

Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## Requirements

### Validated

- ✓ Postgres WAL consumption with pgoutput decoding, TOAST handling, and schema evolution — v1.0
- ✓ Embedded durable Event Log (Badger) with partitioned append, TTL, and deduplication — v1.0
- ✓ Consistent backfills with watermark coordination, keyset cursors, crash recovery — v1.0
- ✓ Partitioned router with per-key ordering, consumer isolation, and poison pill handling — v1.0
- ✓ stdout, SSE, and gRPC output servers with independent consumers and per-consumer cursors — v1.0
- ✓ YAML config with per-table column/row filtering — v1.0
- ✓ Prometheus metrics (lag, throughput, backfill progress, errors, consumer lag) — v1.0
- ✓ /healthz endpoint with real component probes (Badger, SQLite, Postgres) — v1.0
- ✓ Pure Go default build (no CGO required) — v1.0

### Active

- [ ] HA via Postgres advisory lock leader election with shared checkpoint store (Phase 8)
- [ ] MongoDB Change Streams connector with resume tokens and re-snapshot detection (Phase 9)
- [ ] Optional Rust FFI for pgoutput decoding, TOAST cache, and JSON serialization (Phase 10)

### Known Tech Debt (v1.0)

- No HTTP server in stdout mode — `/metrics` and `/healthz` unreachable in default output mode
- `TODO(SRC-06)`: cursor not reset on replication slot loss post-failover (conservative behavior, safe for v1.0)
- `connector.Events()` orphaned exported channel (intentional design, documented safe)

### Out of Scope

- Landing page / marketing site — already built, maintained separately
- Kaptanto Cloud (managed sinks, dashboard, transforms, RBAC, multi-tenant) — SaaS product, not open-source binary
- MySQL connector — future database source, not v1

## Context

**v1.0 shipped 2026-03-16.** 14 phases, 32 plans, 114 commits. Full Postgres CDC pipeline operational.

The full technical specification at `kaptanto-technical-specification.md` remains the authoritative architecture reference.

Key Go packages: jackc/pglogrepl (WAL), jackc/pgx/v5 (Postgres driver), dgraph-io/badger/v4 (Event Log), modernc.org/sqlite (checkpoint/cursor/backfill stores), spf13/cobra (CLI), google.golang.org/grpc, prometheus/client_golang, oklog/ulid.

Performance targets: 500K+ events/sec pure Go, 1.5M+ with Rust FFI. Memory under 100MB for typical workloads.

Integration tests require a live Postgres 14+ instance (`//go:build integration` tag) — not yet wired into CI.

## Constraints

- **Distribution**: Single static binary, zero external dependencies (no Kafka, no JVM)
- **Go version**: 1.22+ required
- **Postgres**: 14-17 with wal_level=logical
- **MongoDB**: 4.0+ replica set or sharded cluster (v1.1)
- **Rust FFI**: Optional, behind build tag (CGO_ENABLED=1 + 'rust' build tag) (v1.1)
- **Checkpoint invariant**: Source LSN/resume token NEVER advanced until event is durably written to Event Log

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Badger v4 for Event Log | Pure Go, LSM tree optimized for write-heavy append, native TTL | ✓ Good — zero issues in v1.0 |
| pgoutput (not wal2json) | Built into Postgres 10+, no extensions required | ✓ Good — cleaner deployment story |
| Advisory locks for HA | Session-scoped, no TTL/clock skew, released on TCP close | — Pending (Phase 8) |
| modernc.org/sqlite for checkpoints | Pure Go SQLite, no CGO dependency for default build | ✓ Good — URI pragma format unreliable; use db.Exec instead |
| Keyset cursors for snapshots | OFFSET breaks on concurrent inserts/deletes | ✓ Good — confirmed correct in backfill engine |
| Watermark dedup for backfills | Prevents stale reads during snapshot+WAL coordination | ✓ Good — BKF-02 fix confirmed semantics correct |
| EventLog as pipeline backbone | Router reads from EventLog.ReadPartition, not connector.Events() channel | ✓ Good — simplifies backpressure, enables crash recovery |
| Decimal phase numbering for gap closure | Clear insertion semantics without renumbering existing phases | ✓ Good — 7 gap-closure phases inserted cleanly |
| Non-blocking AppendAndQueue with drain-or-drop | Prevents WAL receive goroutine stall under slow consumers | ✓ Good — fixed INT-01 blocking channel |
| SetMetrics setter pattern (not constructor param) | Breaks circular dependency between output writers and observability | ✓ Good — used consistently in stdout, SSE, gRPC writers |

---
*Last updated: 2026-03-17 after v1.0 milestone completion*
