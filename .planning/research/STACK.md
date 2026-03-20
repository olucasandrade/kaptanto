# Stack Research

**Domain:** CDC benchmarking suite (v1.2 milestone — adds benchmark harness to existing Kaptanto binary)
**Researched:** 2026-03-20
**Confidence:** HIGH (Docker Compose tool requirements), MEDIUM (load generator choice), HIGH (metrics collection), HIGH (report generation)

> **Scope:** This file covers ONLY the new stack needed for the benchmark suite. The Kaptanto binary stack (Go, pglogrepl, pgx, Badger, etc.) is already validated in PROJECT.md and is not repeated here.

---

## Benchmark Suite Architecture (Overview)

```
bench/
  harness/          -- Docker Compose orchestrator (Go binary: bench/cmd/harness)
  runner/           -- Scenario executor, metrics collector
  report/           -- HTML + Markdown report generator
  load/             -- Load generator (runs inside Docker, pgx-based)

compose/
  docker-compose.benchmark.yml   -- All 5 CDC tools + Postgres + load gen
  configs/
    debezium/       -- application.properties for Debezium Server
    peerdb/         -- PeerDB env vars
    sequin/         -- sequin.yml
```

The harness binary drives the full lifecycle:
1. Start Docker Compose services
2. Wait for health checks
3. Run load generator scenarios
4. Collect metrics from Docker stats API + per-tool endpoints
5. Generate self-contained HTML + Markdown report

---

## Section 1: Competitor Tool Requirements

### Debezium (Postgres CDC)

**Verdict:** Use `quay.io/debezium/server` (Debezium Server, standalone, no Kafka required). This is the correct choice for a benchmark: Debezium Server streams CDC events to an HTTP sink without requiring Kafka, Zookeeper, or KRaft.

**Critical finding:** Debezium 3.x images are no longer published to Docker Hub. Pull from `quay.io/debezium/server:3.4`. Using the full Kafka+Connect stack would add 3 extra containers (Kafka broker, Kafka Connect) and is explicitly wrong for a fair single-process comparison.

| Service | Image | Dependencies |
|---------|-------|--------------|
| Debezium Server | `quay.io/debezium/server:3.4` | None — HTTP sink writes to harness collector |
| Postgres (shared) | `postgres:16` | wal_level=logical, shared with all tools |

**Config:** `application.properties` with `debezium.source.connector.class=io.debezium.connector.postgresql.PostgresConnector`, offset storage to file, HTTP sink pointed at harness collector.

**JVM footprint:** Debezium Server is Quarkus-based. Expect ~256MB RSS at idle, ~400MB under load. The JVM (GraalVM native or HotSpot) is baked into the image; no separate JVM service needed.

**Confidence:** HIGH — verified via official Debezium Server docs and quay.io image registry.

### PeerDB (Postgres CDC)

**Verdict:** PeerDB's Docker Compose requires Temporal (workflow orchestration) + flow-worker + catalog Postgres + MinIO. This is a heavyweight stack — 5+ containers for PeerDB alone.

**Critical finding:** PeerDB is NOT a lightweight CDC tool. Its docker-compose.yml includes `temporalio/auto-setup`, `flow-api`, `flow-snapshot-worker`, `flow-worker`, a catalog Postgres, and MinIO. This is the correct representation for a fair benchmark — PeerDB's operational cost IS part of what's being measured.

| Service | Image | Notes |
|---------|-------|-------|
| Temporal | `temporalio/auto-setup:latest` | Workflow orchestration |
| PeerDB flow-api | `ghcr.io/peerdb-io/flow-api:stable` | REST API |
| PeerDB flow-worker | `ghcr.io/peerdb-io/flow-worker:stable` | CDC worker |
| PeerDB flow-snapshot-worker | `ghcr.io/peerdb-io/flow-snapshot-worker:stable` | Snapshot worker |
| Catalog Postgres | `postgres:16` | PeerDB internal state (separate from benchmark Postgres) |
| MinIO | `minio/minio` | Intermediate storage for CDC batches |

**Resource measurement note:** For the benchmark, CPU% and RSS must sum ALL PeerDB-related containers (temporal + 3 flow containers + MinIO). PeerDB's resource footprint is the composite.

**Confidence:** HIGH — verified via github.com/PeerDB-io/peerdb/blob/main/docker-compose.yml.

### Maxwell's Daemon (MySQL CDC only)

**Critical finding:** Maxwell's Daemon does NOT support Postgres. It is a MySQL-to-Kafka tool only. The PROJECT.md lists Maxwell's Daemon as a benchmark target, but this is incorrect. Maxwell reads MySQL binlogs and has no Postgres WAL support — confirmed by the official issue tracker (issue #434, closed 2023: "no plans to introduce postgres support").

**Recommendation:** Replace Maxwell's Daemon in the benchmark with a Postgres-compatible competitor. The most appropriate replacement that completes the comparison matrix:

| Option | Why Good Replacement |
|--------|---------------------|
| **pglogical** | Native Postgres logical replication extension — low overhead, shows "raw replication baseline" |
| **Estuary Flow** | Go-based, Postgres CDC, Docker-deployable — direct Kaptanto competitor |
| **Watermill CDC** | Lightweight Go CDC library — shows minimal-footprint comparison |

**Recommended replacement: Estuary Flow** (`ghcr.io/estuary/flow`) — Go binary, Postgres CDC, Docker-deployable, direct comparable. However, this requires research validation in the milestone plan phase.

**Alternative if keeping Maxwell:** Benchmark Maxwell against MySQL (not Postgres) as a separate "MySQL CDC" scenario, making the benchmark multi-database. This adds scope and is not recommended for v1.2.

**Confidence for Maxwell-not-Postgres:** HIGH — official GitHub issue #434 confirmed by maintainer.

### Sequin (Postgres CDC)

**Verdict:** Sequin requires Postgres + Redis. Two containers. Relatively lightweight compared to PeerDB.

| Service | Image | Notes |
|---------|-------|-------|
| Sequin | `sequin/sequin:latest` (or `ghcr.io/sequinstream/sequin`) | Elixir/OTP binary, ~128MB RSS |
| Redis | `redis:7` | Required for exactly-once delivery deduplication |
| Postgres (shared) | `postgres:16` | Same shared Postgres as other tools |

**Elixir/OTP note:** Sequin is an Elixir application. The Docker image is self-contained (BEAM VM included). No separate Elixir installation required. Image size ~150MB.

**Confidence:** HIGH — verified via github.com/sequinstream/sequin/blob/main/docker/docker-compose.yaml.

---

## Section 2: Load Generator

### Recommended: Custom Go binary using jackc/pgx/v5

**Use a custom Go load generator, not pgbench.** Rationale:

1. **CDC-specific workload control:** pgbench generates TPC-B transactions (SELECT + UPDATE + INSERT mix). CDC benchmarks need pure INSERT workloads with controlled schemas that produce deterministic WAL events with known primary keys and TOAST content. pgbench cannot generate TOAST-heavy rows or simulate burst/batch patterns precisely.

2. **Timestamp injection:** The load generator must stamp each row with `inserted_at = NOW()` so the harness can compute end-to-end latency by comparing insertion time to event receipt time at the CDC tool. pgbench lacks this.

3. **Scenario control:** The harness binary drives the load generator via signals or a simple HTTP control plane. A custom Go binary integrates cleanly.

4. **pgx is already a dependency:** Kaptanto already uses pgx/v5. The load generator can use the same library — zero new dependency.

**Library:**

| Library | Version | Purpose |
|---------|---------|---------|
| jackc/pgx/v5 | v5.8.0 (already in go.mod) | Bulk INSERT via `pgx.Batch` or `COPY` protocol |

**Load generator schema (minimal, supports all scenarios):**

```sql
CREATE TABLE bench_events (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payload     TEXT,                    -- variable size for TOAST testing
  inserted_at TIMESTAMPTZ DEFAULT NOW()
);
```

**Scenarios implemented by load generator:**

| Scenario | What it does |
|----------|--------------|
| `steady` | 1K rows/sec for 60s — measures sustained throughput |
| `burst` | 0 rows for 5s, then 50K rows in 1s — measures burst handling |
| `large_batch` | Single transaction with 100K rows — measures batch processing |
| `toast` | Rows with 12KB payload fields — triggers Postgres TOAST |
| `idle` | No inserts for 60s — measures idle resource usage |
| `crash_recovery` | Insert 10K rows, kill CDC tool, restart, continue — measures recovery time |

**Alternative considered:** k6 with postgres extension — rejected because k6 is Node.js-based, adds a large runtime, and the postgres extension is unofficial. pgbench rejected as explained above. sysbench rejected as C-based, harder to integrate with the Go harness.

**Confidence:** MEDIUM — based on established benchmarking practice and pgx documentation. Custom load generator is standard practice for CDC benchmarks (confirmed by PeerDB's own benchmark tooling pattern).

---

## Section 3: Metrics Collection (CPU / Memory / Throughput)

### Recommended: Docker Engine API via docker/docker client

**Library:**

| Library | Version | Purpose |
|---------|---------|---------|
| `github.com/docker/docker` | v28.x (use `docker/docker/client`) | Container stats (CPU%, RSS, net I/O) |

**Method:** `client.ContainerStatsOneShot(ctx, containerID)` — polls each container every 1 second. Parses the `container.Stats` struct for:
- `CPUStats.CPUUsage.TotalUsage` (delta / system delta × num CPUs = CPU%)
- `MemoryStats.Usage` — RSS in bytes
- `MemoryStats.Stats["cache"]` — subtract cache from RSS for real RSS

**Note on CPU% calculation:** Docker's stats API returns raw nanosecond counters. CPU% = `(cpuDelta / systemDelta) * numCPUs * 100`. This must be calculated per-poll, not accumulated. The `ContainerStatsOneShot` method is preferred over streaming (`ContainerStats`) because it avoids the first-sample warmup delay.

**Note on PeerDB multi-container:** Aggregate all PeerDB containers (temporal, flow-worker, flow-snapshot-worker, minio) into a single "PeerDB" resource measurement by summing CPU% and RSS across all container IDs in the PeerDB group.

**Confidence:** HIGH — Docker Engine API v1.53 is stable, verified at pkg.go.dev/github.com/docker/docker/client.

### Throughput and Latency: HdrHistogram

For per-CDC-tool event throughput and end-to-end latency percentiles:

| Library | Version | Purpose |
|---------|---------|---------|
| `github.com/HdrHistogram/hdrhistogram-go` | v1.2.0 | p50/p95/p99 latency histograms |

**How it works:**
- Each CDC tool outputs events to an HTTP endpoint or stdout sink managed by the harness
- Harness records `(event_id, recv_time)` pairs
- Joins against `(event_id, inserted_at)` from the load generator's INSERT log
- Feeds `recv_time - inserted_at` into HdrHistogram per tool
- Samples throughput counter every 1 second → events/sec time series

**HdrHistogram over simple percentile sort:** HdrHistogram uses O(1) recording and constant memory. For high-throughput CDC scenarios (500K+ events/sec), sorting a slice for percentiles is impractical. HdrHistogram handles nanosecond precision with configurable significant digits.

**Confidence:** HIGH — verified at pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go (v1.2.0, Nov 2025).

---

## Section 4: HTML Report Generation

### Recommended: go-echarts v2 + Go html/template

| Library | Version | Purpose |
|---------|---------|---------|
| `github.com/go-echarts/go-echarts/v2` | v2.7.1 | Chart generation (embeds ECharts v5.4.3 JS inline) |
| `html/template` (stdlib) | Go stdlib | Report page scaffolding |
| `embed` (stdlib) | Go 1.16+ | Embed CSS and chart JS into binary |

**Why go-echarts over alternatives:**

- **Self-contained output:** go-echarts renders ECharts JS inline in the HTML file. No CDN, no external URLs. The report works offline. This matches the requirement "no external dependencies" exactly.
- **Go-native:** The chart is defined in Go code — bar charts, line charts, and grouped comparisons are all first-class. No JSON-to-JS bridge needed.
- **ECharts quality:** Apache ECharts (the underlying JS library) is production-quality, widely used, and renders line/bar/box charts needed for throughput time series and latency box plots.
- **v2.7.1 is current:** Published March 14, 2026. Actively maintained.

**What NOT to use:**
- **gnuplot:** Requires external binary, not embeddable, poor HTML output.
- **Chart.js via manual embedding:** Works but requires writing JS template strings in Go — verbose and error-prone. go-echarts solves this already.
- **go-charts (wcharczuk):** Generates PNG images, not interactive HTML. PNG charts are not appropriate for a comparison report where users want to zoom/inspect values.
- **Grafana:** Too heavy — requires a running Grafana server. The report must be self-contained.

**Report structure:**

```
index.html (self-contained, ~800KB with inlined ECharts)
  ├── Summary table (tool × metric matrix)
  ├── Throughput time series (line chart, one line per tool)
  ├── Latency box plot (p50/p95/p99 per tool, per scenario)
  ├── CPU% over time (line chart)
  ├── RSS over time (line chart)
  └── Scenario sections (one per: steady, burst, large_batch, toast, idle, crash_recovery)
```

**Confidence:** HIGH — verified at pkg.go.dev/github.com/go-echarts/go-echarts/v2 (v2.7.1, Mar 14, 2026).

---

## Section 5: Markdown Summary Generation

### Recommended: stdlib text/template

No external library needed. The Markdown summary is generated from the same `BenchmarkResult` struct as the HTML report, using Go's `text/template` to produce a `.md` file.

**Template produces:**

```markdown
## Kaptanto CDC Benchmark — 2026-03-20

| Tool        | Events/sec (p50) | Latency p99 | CPU% | RSS (MB) | Recovery (s) |
|-------------|-----------------|-------------|------|----------|-------------|
| Kaptanto    | 512,000         | 4ms         | 8%   | 42       | 1.2         |
| Debezium    | 45,000          | 180ms       | 38%  | 380      | 12.4        |
| ...         |                 |             |      |          |             |
```

**Why not a library:** Markdown generation is string formatting with tabular data. A template file (embedded via `embed`) is sufficient. No library adds value here.

**Output:** `report/summary.md` — designed to be directly included in the project README via a GitHub Actions workflow (deferred to v1.3, but the format should anticipate it).

**Confidence:** HIGH.

---

## Complete Dependency Summary

### New Go dependencies for benchmark suite

```bash
# Metrics collection
go get github.com/docker/docker@v28.5.2+incompatible
go get github.com/docker/distribution@latest      # required by docker/docker
go get github.com/opencontainers/image-spec@latest # required by docker/docker

# Latency histograms
go get github.com/HdrHistogram/hdrhistogram-go@v1.2.0

# HTML report charts
go get github.com/go-echarts/go-echarts/v2@v2.7.1
```

### No new dependencies (use stdlib or existing)

| Need | Solution |
|------|----------|
| Load generator | jackc/pgx/v5 (already in go.mod) |
| Markdown output | text/template (stdlib) |
| HTML scaffolding | html/template (stdlib) |
| Embedded assets | embed (stdlib) |
| Docker Compose orchestration | os/exec + `docker compose` CLI (stdlib) |
| HTTP control server (load gen) | net/http (stdlib) |

---

## Docker Compose Service Count Summary

| Tool | Containers | Notes |
|------|-----------|-------|
| Kaptanto | 1 | Single binary, zero infra deps |
| Debezium Server | 1 | Standalone, HTTP sink, no Kafka |
| PeerDB | 6 | temporal, flow-api, flow-worker, flow-snapshot-worker, catalog-pg, minio |
| Sequin | 2 | sequin, redis |
| Maxwell's Daemon | N/A — EXCLUDE | MySQL-only, no Postgres support |
| Shared Postgres | 1 | postgres:16, wal_level=logical |
| Load generator | 1 | Custom Go binary |
| Harness/collector | 1 | HTTP event sink + stats collector |

**Total: ~13 containers.** This is significant — Docker resource limits must be set carefully to avoid host memory saturation during benchmarks.

---

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| Debezium deployment | Debezium Server (standalone) | Debezium + Kafka + KRaft | Kafka adds 2+ containers, unfair comparison, not what most Postgres CDC users run |
| Load generator | Custom pgx Go binary | pgbench | pgbench lacks INSERT-only mode with timestamp injection and TOAST payload control |
| Load generator | Custom pgx Go binary | k6 | Node.js runtime, unofficial Postgres plugin, complex integration with Go harness |
| Load generator | Custom pgx Go binary | sysbench | C binary, no Docker image, hard to integrate |
| Container metrics | docker/docker client | cAdvisor | cAdvisor requires its own container and Prometheus scraping — over-engineered for a standalone benchmark |
| Container metrics | docker/docker client | docker stats CLI + shell parsing | Fragile text parsing, no sub-second resolution |
| HTML charts | go-echarts | Chart.js raw embedding | Requires writing JS template strings in Go, more code, no added benefit |
| HTML charts | go-echarts | gnuplot | Requires system binary, no HTML output, not embeddable |
| HTML charts | go-echarts | go-charts | PNG output only, not interactive |
| Latency percentiles | hdrhistogram-go | sort.Slice + percentile calc | O(n log n) on 500K+ samples is impractical; HdrHistogram is O(1) record, O(1) query |
| Markdown generation | text/template | goldmark | Goldmark parses Markdown, doesn't generate it; text/template is sufficient |

---

## What NOT to Add

| Avoid | Why | Impact |
|-------|-----|--------|
| Full Kafka stack (broker + ZK/KRaft) | Debezium Server HTTP sink makes Kafka unnecessary; adding it inflates Debezium's resource cost unfairly and adds 2 containers | Benchmark validity |
| Maxwell's Daemon | MySQL-only — no Postgres support; including it would require a separate MySQL instance and different WAL semantics | Scope creep, invalid comparison |
| External chart hosting (Highcharts CDN, Plotly CDN) | Report must be self-contained for offline use and GitHub embedding | Report requirement violation |
| Grafana/Prometheus stack | Over-engineered for a benchmark that runs once and produces a static report | 3+ extra containers, startup complexity |
| Kubernetes deployment | Benchmark should run with `docker compose up`, not require a k8s cluster | Accessibility requirement |
| PeerDB Temporal Cloud | Self-hosted Temporal is correct for a fair benchmark; cloud adds network latency variable | Measurement validity |

---

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| go-echarts/v2 v2.7.1 | Go 1.18+ | html/template integration works in Go 1.22+ |
| docker/docker v28.x | Docker Engine API 1.53 | Use `NewClientWithOpts(client.FromEnv)` |
| hdrhistogram-go v1.2.0 | Go 1.16+ | MIT license |
| jackc/pgx/v5 v5.8.0 | Postgres 14-17 | Already in go.mod — no new version needed |

---

## Sources

- [Debezium Server docs](https://debezium.io/documentation/reference/stable/operations/debezium-server.html) — confirmed standalone operation, HTTP sink, Postgres connector; HIGH confidence
- [Debezium quay.io migration](https://debezium.io/blog/2024/09/18/quay-io-reminder/) — Docker images moved from Docker Hub to quay.io in 2024; HIGH confidence
- [Debezium 3.4.0.Final release](https://debezium.io/blog/2025/12/16/debezium-3-4-final-released/) — latest stable version; HIGH confidence
- [PeerDB docker-compose.yml](https://github.com/PeerDB-io/peerdb/blob/main/docker-compose.yml) — confirmed Temporal + MinIO + 3 flow containers; HIGH confidence
- [Maxwell issue #434](https://github.com/zendesk/maxwell/issues/434) — confirmed no Postgres support, issue closed 2023; HIGH confidence
- [Sequin docker-compose.yaml](https://github.com/sequinstream/sequin/blob/main/docker/docker-compose.yaml) — confirmed Postgres + Redis; HIGH confidence
- [pkg.go.dev/github.com/go-echarts/go-echarts/v2](https://pkg.go.dev/github.com/go-echarts/go-echarts/v2) — v2.7.1, Mar 14, 2026; HIGH confidence
- [pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go](https://pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go) — v1.2.0, Nov 9, 2025; HIGH confidence
- [pkg.go.dev/github.com/docker/docker/client](https://pkg.go.dev/github.com/docker/docker/client) — v28.5.2, ContainerStatsOneShot method confirmed; HIGH confidence

---

*Stack research for: Kaptanto v1.2 CDC Benchmark Suite*
*Researched: 2026-03-20*
