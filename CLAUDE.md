# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It streams changes from Postgres (WAL logical replication) and MongoDB (Change Streams) via stdout, SSE, or gRPC. The name means "who captures" in Esperanto.

The implementation is complete. `kaptanto-technical-specification.md` remains the authoritative architecture reference.

## Build & Test

```bash
make build          # CGO_ENABLED=0 static binary (default, cross-platform)
make test           # all tests, CGO_ENABLED=0
make test-race      # race detector (requires CGO)
make build-rust     # optional Rust-accelerated binary (requires Rust 1.77+, cargo, cbindgen)
make verify-no-cgo  # cross-compile linux/amd64 + darwin/arm64 to confirm no CGO leakage
```

Run a single test:
```bash
go test ./internal/router -run TestPerKeyOrdering -v
go test ./internal/cmd -run TestFlagSource -v
```

Pure Go build (CGO_ENABLED=0) is enforced for static distribution. The Rust FFI path requires the `build_ffi` build tag and CGO.

## Architecture

### Data Flow

```
Source (Postgres WAL / MongoDB Change Stream)
  → Parser (pgoutput/parser.go or mongodb/normalizer.go)
      → EventLog (badger.go, 64 partitions, TTL, dedup by IdempotencyKey)
          → Checkpoint saved (ONLY after Append succeeds — CHK-01)
              → Router (fan-out to consumers, per-key ordering — RTR-04)
                  → Output (stdout NDJSON / SSE /events / gRPC CdcStream)
```

Backfill runs concurrently with WAL streaming. The WatermarkChecker discards snapshot rows where a WAL event with a higher LSN already exists for the same key (same 64-partition hash as EventLog — BKF-02).

### Key Packages

| Package | Role |
|---|---|
| `internal/cmd/root.go` | Cobra CLI, pipeline assembly, graceful shutdown |
| `internal/event/event.go` | ChangeEvent struct (ULID ID, unified insert/update/delete/read/control) |
| `internal/config/config.go` | YAML + CLI flag merging; CLI flags always win |
| `internal/eventlog/badger.go` | Durable append-only store: FNV-1a partitioned, TTL, seq=0 on dup |
| `internal/source/postgres/connector.go` | Logical replication slot, heartbeats, reconnect backoff |
| `internal/source/mongodb/connector.go` | Change Streams, resume token, snapshot on InvalidResumeToken |
| `internal/parser/pgoutput/parser.go` | WAL → ChangeEvent; RelationCache + TOASTCache |
| `internal/backfill/backfill.go` | Snapshot engine (keyset cursor, watermark check) |
| `internal/router/router.go` | Fan-out, per-key ordering, cursor persistence |
| `internal/output/sse/server.go` | SSE `/events` endpoint with consumer/table/operation filters |
| `internal/output/grpc/server.go` | gRPC Subscribe + Acknowledge RPCs |
| `internal/ha/leader.go` | Postgres advisory lock leader election (~5s failover) |
| `internal/observability/metrics.go` | Custom prometheus.Registry; `/metrics` + `/healthz` |
| `internal/checkpoint/` | SQLite (local) or PostgreSQL (HA) for source LSN + consumer cursors |

### Runtime Data Directory

```
./data/
├── events/        # Badger event log
├── checkpoint.db  # SQLite: source LSN (non-HA)
├── cursors.db     # SQLite: per-consumer, per-partition delivery positions
└── backfill.db    # SQLite: snapshot progress + watermark state
```

## Critical Invariants

These must never be violated:

1. **CHK-01 — Durability:** Source checkpoint NEVER advances until `EventLog.Append()` returns successfully. Crash → source re-sends → EventLog deduplicates by `IdempotencyKey`.

2. **RTR-04 — Per-key ordering:** Router delivers events for the same primary key in order. A failed delivery blocks that key only; other keys continue. Retry logic in `internal/router/retry.go`.

3. **BKF-02 — Watermark consistency:** WatermarkChecker and EventLog must use the same partition count (64) so FNV-1a hashes are consistent.

4. **TOAST handling:** Postgres UPDATE events may omit unchanged large columns. Parser merges from TOASTCache keyed by `(relation_id, primary_key)`.

5. **Keyset cursors, never OFFSET:** Snapshot pagination uses `internal/backfill/cursor.go`; OFFSET breaks under concurrent writes.

6. **SRC-01 — Connection isolation:** Postgres connector keeps a separate `pgx.Conn` for snapshots; replication connections cannot be reused for regular queries.

## Test Patterns

- Tests use fake implementations (e.g., `fakeConsumer`, `fakeEventLog`) rather than mocks.
- `internal/metrics`: each test creates its own `prometheus.Registry` via `NewKaptantoMetrics()` — no global state.
- `internal/cmd`: tests call `cmd.ExecuteWithArgs(args, out)` which creates a fresh `cobra.Command` — no shared state.
- Router tests pass `nil` for `cursorStore`; the router substitutes a `noopCursorStore` automatically.

## Benchmarking

The `bench/` directory contains a harness that compares Kaptanto vs Debezium, Sequin, and PeerDB. It requires Docker Compose.

```bash
cd bench
docker compose down -v          # REQUIRED before every run (prevents cross-run state contamination)
docker compose up --build -d
# Run a scenario:
go run ./cmd/scenarios -- --scenario steady
docker compose down -v          # clean up after
```

Results are written to `bench/results/`. The rendered report is at `bench/results/REPORT.md`.

Clean-run procedure is mandatory — see memory for details on contamination risk.

## Configuration

YAML config (all fields also available as CLI flags; flags take precedence):

```yaml
source: "postgres://user:pass@host/db"
output: sse          # stdout | sse | grpc
port: 7654
data_dir: /var/lib/kaptanto
retention: 24h

tables:
  - name: public.orders
    columns: [id, status, total]
    where: "status != 'archived'"
```

## Landing Page

`landing/` is a static site with no build step. Open `landing/index.html` directly in a browser. All documentation content lives in `landing/js/main.js` as the `docs` object; the sidebar is defined in the `sidebar` array in the same file.
