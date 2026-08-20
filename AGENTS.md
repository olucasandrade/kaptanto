# Kaptanto — Agent Guide

This file is written for AI coding agents that need to work productively in the Kaptanto repository. It is derived from the actual project contents; do not assume any convention that is not described here or visible in the code.

## Project Overview

Kaptanto (Esperanto for "who captures") is an open-source, single static Go binary for universal database Change Data Capture (CDC). It tails:

- **Postgres** via WAL logical replication (`pgoutput` protocol).
- **MongoDB** via Change Streams (requires a replica set).

Each captured change is normalized into a unified `ChangeEvent`, durably appended to an embedded BadgerDB event log, and only then is the source checkpoint advanced so a crash can never lose an event. An initial backfill snapshot runs concurrently with live streaming; a watermark check discards snapshot rows already superseded by a WAL event. Events are then fanned out, in per-key order, to one of eight outputs:

- `stdout` (NDJSON)
- `sse` (HTTP Server-Sent Events)
- `grpc` (Protocol Buffers `Subscribe` + `Acknowledge` RPCs)
- `kafka`, `nats`, `sqs`, `pubsub`, `rabbitmq` (broker sinks)

The repository is a monorepo that also contains:

- `bench/` — a Docker Compose benchmark harness comparing Kaptanto to Debezium, Sequin, and PeerDB.
- `landing/` — the marketing/docs website, a Qwik City TypeScript app deployed to Cloudflare Pages.
- `examples/` — runnable React + Vite demo applications with their own `docker-compose.yml` files.
- `rust/kaptanto-ffi/` — an optional Rust static library that accelerates the Postgres `pgoutput` parser via CGO.
- `get/` — a tiny Cloudflare Worker that redirects `curl -L get.kaptanto.dev` to the install script.
- `packages/` — the published SDKs: `kaptanto-events` (`@kaptanto/events`, TS types + SSE client), `kaptanto-mastra` (`@kaptanto/mastra`, Mastra adapter), `kaptanto-python` (`kaptanto` on PyPI, pydantic models + httpx SSE client), and `n8n-nodes-kaptanto` (`n8n-nodes-kaptanto` on npm).

## Technology Stack

### Core runtime

- **Go 1.25.8** with `CGO_ENABLED=0` as the default/static distribution constraint.
- **Cobra** for the CLI; **pflag** for flag merging.
- **BadgerDB v4** for the append-only, TTL'd event log (64 FNV-1a partitions).
- **modernc.org/sqlite** for local SQLite stores (checkpoint, cursors, backfill).
- **jackc/pgx/v5** and **jackc/pglogrepl** for Postgres logical replication.
- **mongo-driver/v2** for MongoDB Change Streams.
- **twmb/franz-go** for Kafka; **nats.go/nats-server** for NATS JetStream; **aws-sdk-go-v2** for SQS FIFO; **cloud.google.com/go/pubsub** for Google Pub/Sub; **amqp091-go** for RabbitMQ.
- **grpc**, **prometheus/client_golang**, **oklog/ulid/v2**, **golang.org/x/sync**.

### Optional Rust path

- **Rust 2021 edition** crate in `rust/kaptanto-ffi/`, built as a `staticlib` with `cbindgen`.
- Activated with the `rust` build tag; requires `CGO_ENABLED=1` and is host-only (not cross-compilable).

### Website and examples

- `landing/`: Qwik City + Vite + TypeScript, deployed via `wrangler pages deploy`.
- `examples/*/web/`: React + Vite frontends; `examples/*/api/`: Node backends that consume Kaptanto SSE.
- `get/`: Cloudflare Worker script (`src/index.js`).

## Project Layout

```
├── cmd/kaptanto/          # CLI main()
├── internal/
│   ├── auth/              # Bearer-token auth middleware
│   ├── backfill/          # Snapshot engine + watermark checker
│   ├── checkpoint/        # SQLite/Postgres source + cursor persistence
│   ├── cluster/           # Shared cursor state across nodes
│   ├── cmd/               # Cobra root command + pipeline assembly
│   ├── config/            # YAML + CLI flag merging (single source of truth)
│   ├── event/             # ChangeEvent domain type (leaf package)
│   ├── eventlog/          # BadgerDB durable event log
│   ├── ha/                # Postgres advisory-lock leader election
│   ├── logging/           # slog setup
│   ├── observability/     # Prometheus metrics + /healthz
│   ├── output/            # All consumer implementations
│   │   ├── grpc/          # gRPC server + .proto generated code
│   │   ├── kafka/         # Kafka sink
│   │   ├── nats/          # NATS JetStream sink
│   │   ├── pubsub/        # Google Pub/Sub sink
│   │   ├── rabbitmq/      # RabbitMQ sink
│   │   ├── sqs/           # AWS SQS FIFO sink
│   │   ├── sse/           # HTTP Server-Sent Events server
│   │   └── stdout/        # NDJSON to stdout
│   ├── parser/
│   │   ├── mongodb/       # MongoDB change stream normalizer
│   │   └── pgoutput/      # Postgres pgoutput parser (+ optional Rust FFI)
│   ├── pgident/           # Postgres schema/table identifier parsing + safe quoting (leaf)
│   ├── pk/                # Primary-key extraction utilities
│   ├── redact/            # Secret redaction helpers
│   ├── router/            # Fan-out, per-key ordering, retries, cursor advance
│   ├── source/
│   │   ├── mongodb/       # MongoDB connector
│   │   └── postgres/      # Postgres logical-replication connector
│   └── version/           # Version injected by -ldflags
├── bench/                 # Separate Go module; benchmark harness
├── landing/               # Qwik City website
├── examples/              # Runnable CDC demos
├── rust/kaptanto-ffi/     # Optional Rust staticlib
├── get/                   # Cloudflare Worker install redirect
├── packages/              # Published clients (@kaptanto/events, @kaptanto/mastra, PyPI kaptanto, n8n-nodes-kaptanto)
├── test/e2e/              # Black-box binary tests (build tag `e2e`)
├── scripts/coverage-gate.sh   # Coverage thresholds (CI + local)
├── .golangci.yml          # Linting (complexity + dependency + dead-code rules)
├── .gremlins.yaml         # Mutation-testing configuration
├── .goreleaser.yaml       # Release artifact builds
└── Dockerfile             # Distroless static-binary image
```

## Build and Development Commands

All primary commands are defined in the root `Makefile`.

### Go binary (default path)

```bash
make build              # CGO_ENABLED=0 static binary → ./kaptanto
make verify-no-cgo      # cross-compile linux/amd64, darwin/arm64, windows/amd64
make clean              # remove ./kaptanto and Rust target/
```

### Optional Rust-accelerated binary

```bash
make build-rust         # cargo build staticlib + CGO_ENABLED=1 Go binary
make test-rust          # cargo test + rust-tagged Go structural tests
```

### Website

From `landing/`:

```bash
npm install
npm run dev          # Vite SSR dev server
npm run build        # Cloudflare Pages production build
npm run build.types  # tsc --noEmit
npm run lint         # ESLint over src/**/*.ts*
npm run fmt.check    # Prettier check
npm run test         # vitest run
npm run deploy       # wrangler pages deploy ./dist
```

### Benchmark harness

```bash
cd bench
docker compose down -v          # REQUIRED before every run
docker compose up --build -d
go run ./cmd/scenarios -- --scenario steady
docker compose down -v
```

## Test Commands and Strategies

### Fast local tests

```bash
make test               # all tests, CGO_ENABLED=0, -count=1
make test-race          # race detector, requires CGO
make lint               # golangci-lint over ./internal/... ./cmd/...
make cover              # coverage run with threshold gate
```

Run a single package or test:

```bash
go test ./internal/router -run TestPerKeyOrdering -v -count=1
go test ./internal/cmd -run TestFlagSource -v -count=1
```

### Live-service tests (env-gated)

```bash
# Integration: live Postgres + MongoDB + RabbitMQ
POSTGRES_TEST_DSN=postgres://user:pass@localhost:5432/db \
MONGO_TEST_URI=mongodb://localhost:27017/?replicaSet=rs0 \
RABBITMQ_TEST_URL=amqp://guest:guest@localhost:5672/ \
  make test-integration

# E2E: black-box compiled binary tests
POSTGRES_TEST_DSN=postgres://user:pass@localhost:5432/db \
  make test-e2e

# Mutation testing (gremlins)
make mutation
```

### Coverage policy

`scripts/coverage-gate.sh` is the single source of truth:

- Aggregate threshold over the filtered profile: **75.0%** (`COVERAGE_THRESHOLD`).
- Per-package floor for every non-excluded package: **35.0%** (`PER_PACKAGE_FLOOR`).
- Excluded from gating: `internal/ha`, `internal/source/postgres`, `internal/source/mongodb`, `internal/output/rabbitmq`, `internal/cluster`, `cmd/`, `*.pb.go`, `cmd/kaptanto/main.go` (these need live services or are exercised by e2e).

### Mutation testing

`make mutation` runs `gremlins` over core packages. Per-package efficacy thresholds are the source of truth and match `.github/workflows/mutation.yml`:

- `internal/router`: 90
- `internal/eventlog`: 65
- `internal/backfill`: 75
- `internal/pk`: 40
- `internal/parser/pgoutput`: report-only (its Rust FFI shim is unreachable under the pure-Go build)

## Runtime Architecture and Data Flow

```
Postgres WAL / MongoDB Change Stream
  → Parser (pgoutput or mongodb normalizer)
      → EventLog (BadgerDB, 64 partitions, TTL, dedup by IdempotencyKey)
          → Checkpoint saved ONLY after Append succeeds (CHK-01)
              → Router (fan-out, per-key ordering, retries)
                  → Consumer: stdout / SSE / gRPC / Kafka / NATS / SQS / Pub/Sub / RabbitMQ
```

Key runtime directories under `--data-dir` (default `./data`):

```
data/
├── events/        # Badger event log
├── checkpoint.db  # Source LSN checkpoint (SQLite, or PostgreSQL in HA mode)
├── cursors.db     # Per-consumer, per-partition delivery cursors
└── backfill.db    # Snapshot progress + watermark state
```

### Critical invariants (must never be violated)

- **CHK-01 — Durability:** the source checkpoint advances only after `EventLog.Append()` succeeds.
- **RTR-04 — Per-key ordering:** events for the same primary key are delivered in order; a failure blocks only that key.
- **BKF-02 — Watermark consistency:** EventLog and WatermarkChecker use the same 64 partitions so FNV-1a hashes agree.
- **TOAST handling:** unchanged large columns in Postgres UPDATE events are merged from a `(relation_id, pk)` TOAST cache.
- **Keyset cursors:** snapshot pagination uses keyset cursors (`internal/backfill/cursor.go`), never `OFFSET`.
- **SRC-01:** Postgres replication connections are isolated; snapshots use a separate `pgx.Conn`.
- **DLV-02/03/04:** broker sinks route by CDC key, never retry internally (retry is the router's job), and stamp every message with an idempotency key/header.

The codebase uses three-letter invariant codes (`CHK-01`, `RTR-04`, `BKF-02`, etc.) in comments and tests. A repo-wide grep for `[A-Z]{3}-[0-9]{2}` finds the component-level codes.

## Code Style and Conventions

### Go

- Run `gofmt`; follow standard Go naming and formatting.
- Keep packages small and named by lowercase domain (`eventlog`, `observability`, `backfill`).
- Place tests next to implementation files using Go's `_test.go` naming.
- Prefer table-driven tests for parsers, routers, and connectors.
- Use fake implementations in tests rather than mocks where possible.
- Avoid global state: tests create fresh registries, fresh `cobra.Command` instances, and fresh stores.

### Dependency layers (enforced by `depguard` in `.golangci.yml`)

```
event / pgident  <  eventlog / parser  <  backfill / checkpoint  <  source / output / router  <  cmd
```

Lower layers must never import upper layers. `internal/event` is a pure domain leaf and must not import any other `internal/` package. `internal/pgident` is also a leaf (Postgres identifier parsing/quoting) and likewise must not import any other `internal/` package.

### Complexity

- New functions must stay at or below cyclomatic complexity **25** (`gocyclo` gate).
- Existing functions that exceed this are annotated `//nolint:gocyclo` with a refactor note.
- Additional gates catch dead nil checks (`govet nilness/unreachable`), wasted
  assignments (`wastedassign`), unused params (`unparam`), suspicious constructs
  and duplicate branches (`gocritic`), and stdlib constants (`usestdlibvars`).

### TypeScript / Qwik (landing/)

- Use TypeScript, ESLint, and Prettier defaults.
- Keep route files under `src/routes/` and shared components under `src/components/`.
- Keep framework-consistent, descriptive component filenames.

## Configuration

Runtime config lives in `internal/config/config.go`. All CLI flags can also be expressed in YAML; CLI flags always win. The canonical config shape:

```yaml
source: "postgres://user:pass@host/db"
output: stdout           # stdout | sse | grpc | kafka | nats | sqs | pubsub | rabbitmq
port: 7654
data-dir: ./data
retention: 24h           # 0 applies the built-in 1h default
ha: false
node-id: ""
source-id: default
cluster: false
cluster-dsn: ""
cluster-peers: []
nats-cluster-port: 6222
all-tables: false

auth-token: ""           # prefer KAPTANTO_AUTH_TOKEN env var
insecure: false          # explicit opt-out of TLS/auth; logs a loud warning

server-tls:
  cert-file: ""
  key-file: ""
  client-ca-file: ""     # set for mTLS

tables:
  public.orders:
    columns: [id, status, total]
    where: "status != 'archived'"

sinks:
  kafka:
    bootstrap-servers: ["localhost:9092"]
    topic-template: "cdc.{{.Schema}}.{{.Table}}"
    sasl-mechanism: ""
    tls: { ca-file: "", cert-file: "", key-file: "" }
```

Use `--config <file>` or individual flags. `--source` or `--config` is required.

## Security Considerations

- **Inbound data plane:** SSE and gRPC require a bearer token (`--auth-token` or `KAPTANTO_AUTH_TOKEN`) and, when `--tls-cert`/`--tls-key` are set, TLS. mTLS is enabled with `--tls-client-ca`. `--insecure` disables all of this for local development and logs a warning; it is not for production.
- **Outbound sinks:** each broker sink has its own `tls:` block for CA/mTLS; these are independent of inbound `server-tls`.
- **Redaction:** use `internal/redact` helpers when logging DSNs or secrets.
- **CI security:** `.github/workflows/security.yml` runs `govulncheck` (blocking, symbol-aware) and `gitleaks` (secret scan). `zizmor.yml` audits workflow permissions.
- **Docker image:** the release Dockerfile uses `gcr.io/distroless/static-debian12:nonroot` for a minimal attack surface.

## Deployment and Release

- **GitHub releases:** `goreleaser` builds static binaries for linux, darwin, and windows (amd64/arm64, except windows/arm64) on every `v*` tag (`.goreleaser.yaml`).
- **Docker Hub:** `.github/workflows/release.yml` also builds and pushes `olucasandrade/kaptanto` for `linux/amd64` and `linux/arm64`.
- **Install script:** `scripts/install.sh` is fetched by `curl -L get.kaptanto.dev` (via the `get/` Cloudflare Worker).
- **Landing site:** deployed to Cloudflare Pages from `landing/`. Cloudflare Pages root directory must be `landing`, build command `npm run build`, output directory `dist`.
- **npm/PyPI packages:** `@kaptanto/events`, `@kaptanto/mastra`, `n8n-nodes-kaptanto` (npm) and `kaptanto` (PyPI) publish via tag-triggered workflows (`events-v*`, `mastra-v*`, `n8n-v*`, `python-v*`); versions are independent of the Go binary. See `RELEASING.md` for the required publish order (`@kaptanto/events` first).

## CI Workflows

All workflows live in `.github/workflows/`:

- `ci.yml` — `go vet`, `go test`, `go test -race`, and `verify-no-cgo` cross-compilation.
- `lint.yml` — `golangci-lint` with complexity + dependency rules.
- `coverage.yml` — coverage run + `scripts/coverage-gate.sh`.
- `integration.yml` — live Postgres + MongoDB + RabbitMQ integration tests.
- `e2e.yml` — black-box binary tests against live Postgres.
- `mutation.yml` — `make mutation` with ratcheted per-package thresholds.
- `rust-test.yml` — `cargo test` and rust-tagged Go structural tests.
- `rust-audit.yml` — Rust CVE audit.
- `landing.yml` — lint, typecheck, build, vitest, and render smoke for `landing/`.
- `security.yml` — `govulncheck` and `gitleaks`.
- `zizmor.yml` — workflow permission audit.
- `benchmark.yml` — benchmark harness runs.
- `release.yml` — `goreleaser` + Docker Hub publish.
- `ts.yml` — build + vitest for `packages/kaptanto-events`, `packages/kaptanto-mastra`, `packages/n8n-nodes-kaptanto`, plus fixture drift (`make fixtures`).
- `python.yml` — pytest matrix (3.10–3.13) for `packages/kaptanto-python`, plus a core-only (no langchain) acceptance job.
- `npm-publish.yml` — tag-triggered (`events-v*`/`mastra-v*`/`n8n-v*`) npm publish with OIDC provenance.
- `pypi-publish.yml` — tag-triggered (`python-v*`) PyPI publish via trusted publishing (GitHub environment `pypi`).

## Commit and Pull Request Conventions

Recent history follows **Conventional Commits** (e.g., `feat: ...`, `chore: ...`, `docs(phase-13): ...`). Keep subjects imperative and specific to one change.

PRs should include:

- A short problem statement.
- Implementation summary.
- Test evidence.
- Linked issues.
- Screenshots or short recordings for `landing/` UI changes.
- Setup notes for benchmark or replication-related work.

## Useful Reference Files

- `README.md` — user-facing quick start, feature list, and flag reference.
- `CLAUDE.md` — architecture/package reference and critical invariants.
- `docs/architecture/technical-specification.md` — authoritative architecture specification.
- `DEMO_PLAYBOOK.md` — demo and presentation guidance.
- `CHANGELOG.md` — release history.
