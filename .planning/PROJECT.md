# Kaptanto

## What This Is

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It connects to Postgres (WAL logical replication) and MongoDB (Change Streams) and streams a unified JSON event feed via stdout, SSE, or gRPC — with a durable embedded event log, consistent backfills, high-availability leader election, and optional Rust FFI acceleration. Language-agnostic: any stack can consume events without an SDK.

**v1.0 shipped:** Full Postgres CDC pipeline — 10,749 LOC Go, 114 commits, 9 days.
**v1.1 shipped:** HA leader election, MongoDB connector, Rust FFI acceleration — 14,209 LOC (Go + Rust), 33 commits, 3 days.
**v1.2 shipped:** Benchmark suite — Docker Compose harness, 5 benchmark scenarios, self-contained HTML report with Chart.js — 3,727 LOC Go (bench/), 1 day.

## Core Value

Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## Current State

**v1.2 shipped 2026-03-21.** 21 phases total, 50 plans. Full benchmark suite: `cd bench && docker compose up` starts the full harness; `./scenarios` runs all 5 scenarios; `./reporter` generates a self-contained HTML report with Chart.js charts comparing Kaptanto, Debezium, Sequin, and PeerDB.

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
- ✓ HA via Postgres advisory lock leader election with shared checkpoint store — v1.1
- ✓ MongoDB Change Streams connector with resume tokens and automatic re-snapshot — v1.1
- ✓ Optional Rust FFI for pgoutput decoding, TOAST cache, and JSON serialization — v1.1
- ✓ `--ha` with MongoDB source returns clear error (INT-03) — v1.1
- ✓ Docker Compose benchmark harness (13 services, all pinned, healthchecked) — v1.2
- ✓ Load generator with 4 scenario modes (steady, burst, large-batch, idle), 10k–50k ops/s — v1.2
- ✓ Metrics collector with 4 per-tool adapters (SSE, HTTP, HTTP, Kafka), channel-serialized NDJSON — v1.2
- ✓ Docker stats poller (VmRSS via host /proc, CPU% via docker stats) every 2s — v1.2
- ✓ 5 benchmark scenarios orchestrated end-to-end with crash+recovery detection — v1.2
- ✓ Self-contained HTML report with Chart.js (208KB inlined, no CDN), 7 charts — v1.2
- ✓ Markdown summary (REPORT.md) with percentile tables auto-generated — v1.2

### Active

*(none — planning next milestone)*

### Known Tech Debt (v1.1)

- No HTTP server in stdout mode — `/metrics` and `/healthz` unreachable in default output mode
- `TODO(SRC-06)`: cursor not reset on replication slot loss post-failover (conservative, safe)
- Health probe named `postgres` at `root.go` calls `pgx.Connect` unconditionally — for MongoDB deployments, `/healthz` will always report the postgres probe as unhealthy. Fix: make the postgres health probe conditional on `cfg.SourceType() == "postgres"`
- Integration tests require live Postgres 14+ (`//go:build integration`) — not yet wired into CI

### Out of Scope

- Landing page / marketing site — already built, maintained separately
- Kaptanto Cloud (managed sinks, dashboard, transforms, RBAC, multi-tenant) — SaaS product
- MySQL connector — future database source, not yet planned
- Management REST API (OPS-01) — deferred to v1.2
- Docker multi-stage build (DST-01) — deferred
- GitHub Actions CI (DST-04) — deferred

## Context

**v1.1 shipped 2026-03-20.** 18 phases total, 42 plans, ~147 commits. Full Postgres + MongoDB CDC pipeline with HA and optional Rust acceleration.

Tech stack: Go 1.22+, jackc/pglogrepl (WAL), jackc/pgx/v5 (Postgres driver), go.mongodb.org/mongo-driver (MongoDB), dgraph-io/badger/v4 (Event Log), modernc.org/sqlite (checkpoints/cursors), spf13/cobra (CLI), google.golang.org/grpc, prometheus/client_golang, oklog/ulid.

Rust FFI: kaptanto-ffi staticlib (`rust/kaptanto-ffi/`) — decoder.rs, toast.rs, serializer.rs, cbindgen-generated header. Activated via `CGO_ENABLED=1` + `rust` build tag.

Performance targets: 500K+ events/sec pure Go, 1.5M+ with Rust FFI. Memory under 100MB for typical workloads.

The full technical specification at `kaptanto-technical-specification.md` remains the authoritative architecture reference.

## Constraints

- **Distribution**: Single static binary, zero external dependencies (no Kafka, no JVM)
- **Go version**: 1.22+ required
- **Postgres**: 14-17 with wal_level=logical
- **MongoDB**: 4.0+ replica set or sharded cluster (v1.1)
- **Rust FFI**: Optional, behind `rust` build tag (CGO_ENABLED=1) — pure Go binary is the default
- **Checkpoint invariant**: Source LSN/resume token NEVER advanced until event is durably written to Event Log

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Badger v4 for Event Log | Pure Go, LSM tree optimized for write-heavy append, native TTL | ✓ Good — zero issues in v1.0/v1.1 |
| pgoutput (not wal2json) | Built into Postgres 10+, no extensions required | ✓ Good — cleaner deployment story |
| Advisory locks for HA | Session-scoped, no TTL/clock skew, released on TCP close | ✓ Good — automatic takeover confirmed in v1.1 |
| modernc.org/sqlite for checkpoints | Pure Go SQLite, no CGO dependency for default build | ✓ Good — URI pragma format unreliable; use db.Exec instead |
| Keyset cursors for snapshots | OFFSET breaks on concurrent inserts/deletes | ✓ Good — confirmed correct in backfill engine |
| Watermark dedup for backfills | Prevents stale reads during snapshot+WAL coordination | ✓ Good — BKF-02 fix confirmed semantics correct |
| EventLog as pipeline backbone | Router reads from EventLog.ReadPartition, not connector.Events() channel | ✓ Good — simplifies backpressure, enables crash recovery |
| Decimal phase numbering for gap closure | Clear insertion semantics without renumbering existing phases | ✓ Good — 8 gap-closure phases inserted cleanly (v1.0 + v1.1) |
| Non-blocking AppendAndQueue with drain-or-drop | Prevents WAL receive goroutine stall under slow consumers | ✓ Good — fixed INT-01 blocking channel |
| SetMetrics setter pattern (not constructor param) | Breaks circular dependency between output writers and observability | ✓ Good — used consistently in stdout, SSE, gRPC writers |
| Rust FFI behind build tag (not default) | Keeps default binary pure Go, no CGO; Rust acceleration opt-in | ✓ Good — zero impact on default build path |
| Structural equality for FFI tests (not bytes.Equal) | Go map JSON serialization is non-deterministic; field comparison is stable | ✓ Good — tests pass reliably across both build paths |
| Postgres store for HA checkpoint (not SQLite) | Shared state requires a shared store; SQLite is per-instance | ✓ Good — PostgresStore implements CheckpointStore cleanly |

---
*Last updated: 2026-03-20 — v1.2 milestone started*
