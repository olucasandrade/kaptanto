# Architecture Research

**Domain:** CDC benchmarking suite (bench/ directory, Docker Compose harness, metrics collection, report generation)
**Researched:** 2026-03-20
**Confidence:** HIGH

---

> **Note:** This file was originally written for the core Kaptanto binary (2026-03-07) and has been superseded for the v1.2 Benchmark Suite milestone. The v1.2 research focuses on the `bench/` directory architecture. The original binary architecture remains authoritative in `kaptanto-technical-specification.md`.

---

## System Overview

The benchmark suite is a self-contained sub-system that lives at `bench/` in the repo root. It orchestrates five CDC tools against a shared Postgres instance, drives load, collects metrics, and produces a report. It does not modify any code in `cmd/kaptanto` or `internal/`.

```
bench/
  docker-compose.yml          <- harness: postgres, kaptanto, debezium, peerdb,
                                          maxwell, sequin, load-gen, metrics-collector
  Makefile                    <- bench targets: up, run, report, clean
  cmd/
    loadgen/                  <- Go binary: inserts rows at configurable rate
    collector/                <- Go binary: HTTP adapter per CDC tool, writes JSONL metrics
    reporter/                 <- Go binary: reads JSONL, emits HTML + Markdown report
  config/
    kaptanto.yaml             <- kaptanto config for bench environment
    debezium-connector.json   <- Debezium Postgres connector registration payload
    sequin.yaml               <- Sequin CDC source + HTTP sink config
  scenarios/
    steady.sh                 <- 30s at target TPS
    burst.sh                  <- ramp from 0 to 10x TPS over 10s
    large_batch.sh            <- single INSERT of 50K rows
    crash_recovery.sh         <- kill + restart CDC tool, measure recovery time
    idle.sh                   <- 30s at 0 TPS (resource baseline)
  results/                    <- written at runtime (gitignored)
    metrics.jsonl             <- raw event metrics (one JSON object per line)
    docker_stats.jsonl        <- raw docker stats output (one JSON object per line)
    report.html               <- self-contained HTML report
    report.md                 <- Markdown summary
```

### Component Responsibilities

| Component | Responsibility | New vs Modified |
|-----------|---------------|-----------------|
| `bench/docker-compose.yml` | Spin up all 5 CDC tools + shared Postgres + load-gen + collector against a common network | New |
| `bench/cmd/loadgen` | INSERT rows into Postgres at a configurable rate (--tps, --duration, --table) | New |
| `bench/cmd/collector` | Connect to each CDC tool's output channel, measure per-event latency, write JSONL | New |
| `bench/cmd/reporter` | Read `metrics.jsonl` + `docker_stats.jsonl`, compute p50/p95/p99, emit HTML+MD | New |
| `bench/scenarios/*.sh` | Orchestrate a full scenario: set load, wait, trigger events, collect | New |
| `bench/Makefile` | Provide `make bench-up`, `make bench-run`, `make bench-report`, `make bench-clean` | New |
| `cmd/kaptanto` | Unchanged — used as-is via Docker image built from repo root | Not modified |
| `internal/` packages | Unchanged | Not modified |

---

## Question 1: bench/ Directory Organization

**Recommendation: separate Go module inside bench/, Makefile targets at repo root.**

Use a separate Go module (`bench/go.mod`, module `github.com/kaptanto/kaptanto/bench`). Rationale:

- The bench suite has different dependencies (chart generation helpers, Kafka consumer library for Debezium/Sequin, HTTP client) that should not pollute the main module's `go.sum`.
- The three binaries (`loadgen`, `collector`, `reporter`) can be built independently without triggering a full kaptanto rebuild.
- CI can lint/test the bench module separately from the production code.
- A separate module makes it obvious the bench suite is a development/evaluation tool, not part of the production binary.

The repo-root `Makefile` gets new bench-prefixed targets that delegate into `bench/Makefile`:

```makefile
# Repo root Makefile additions
bench-up:
	$(MAKE) -C bench up

bench-run:
	$(MAKE) -C bench run

bench-report:
	$(MAKE) -C bench report

bench-clean:
	$(MAKE) -C bench clean
```

Inside `bench/Makefile`:

```makefile
up:
	docker compose -f docker-compose.yml up -d --wait

run: up
	bash scenarios/steady.sh
	bash scenarios/burst.sh
	bash scenarios/large_batch.sh
	bash scenarios/crash_recovery.sh
	bash scenarios/idle.sh

report:
	go run ./cmd/reporter --metrics results/metrics.jsonl \
	    --docker-stats results/docker_stats.jsonl \
	    --out-html results/report.html \
	    --out-md results/report.md

clean:
	docker compose -f docker-compose.yml down -v
	rm -rf results/*.jsonl results/*.html results/*.md
```

**Structure rationale:**

- `bench/cmd/` follows the same layout convention as the main module's `cmd/kaptanto/` — one directory per binary.
- `bench/config/` holds CDC-tool-specific configuration files (not Go code) needed at Docker Compose startup time. These are volume-mounted into the respective containers.
- `bench/scenarios/` contains shell scripts (not Go) because scenario orchestration is inherently sequential, time-based, and mixes `docker compose kill`, `sleep`, and process coordination in ways that are simpler in bash than in a Go program with subprocess management.
- `bench/results/` is gitignored. Everything in it is ephemeral output from a run.

---

## Question 2: Metrics Consumer Architecture — Per-Tool Adapters Required

**Each CDC tool needs a separate consumer adapter inside `bench/cmd/collector`.** A generic HTTP consumer cannot work for all five because each tool delivers events through a fundamentally different protocol:

| Tool | Delivery Mechanism | Consumer Approach |
|------|--------------------|-------------------|
| Kaptanto | SSE (HTTP GET `/events`, `text/event-stream`) | Go SSE client — read `bufio.Scanner` over HTTP response body, parse `data:` lines |
| Debezium | HTTP POST webhook (push model, `debezium.sink.type=http`) | Go HTTP server listening on a port; Debezium POSTs batches of events to it |
| PeerDB | Kafka topic (only Kafka, Clickhouse, etc. supported natively; no HTTP push) | Go Kafka consumer using `github.com/segmentio/kafka-go` reading from KRaft broker |
| Maxwell's Daemon | Kafka topic (`--producer=kafka`) | Go Kafka consumer, same library |
| Sequin | HTTP POST webhook (Sequin Stream sink or webhook sink) | Go HTTP server listening on a port; Sequin POSTs batches |

**Implications:**

1. The collector binary must embed both an HTTP server (for Debezium and Sequin push) and HTTP/SSE client (for Kaptanto pull) and a Kafka consumer (for PeerDB and Maxwell).

2. The collector starts all five adapters concurrently as goroutines, each writing to a shared results file (`results/metrics.jsonl`) protected by a mutex or buffered channel.

3. Each incoming event must be timestamped with `received_at` (nanosecond wall clock at arrival) and must carry the `written_at` timestamp inserted by the load generator into the row. End-to-end latency = `received_at - written_at`.

4. The load generator inserts a `_bench_ts BIGINT` column (nanosecond Unix timestamp of insert) into the target table. Each CDC tool propagates this column through to its consumer. The collector reads it from the event payload.

**Adapter interface (conceptual):**

```go
// bench/cmd/collector/adapter.go
type Adapter interface {
    Name() string
    Start(ctx context.Context, out chan<- MetricEvent) error
}

// Implementations:
// KaptantoSSEAdapter  — SSE client connecting to kaptanto:7700/events
// DebeziumHTTPAdapter — HTTP server on :8001, receives Debezium POSTs
// PeerDBKafkaAdapter  — Kafka consumer on broker:9092, topic "peerdb.public.bench"
// MaxwellKafkaAdapter — Kafka consumer on broker:9092, topic "maxwell"
// SequinHTTPAdapter   — HTTP server on :8002, receives Sequin POSTs
```

**Maxwell caveat:** Maxwell only supports MySQL, not Postgres. The benchmark suite compares Postgres CDC tools. Maxwell must be excluded or tested against a MySQL sidecar. The recommended approach is to **exclude Maxwell** from the benchmark (document this in the report) or include a MySQL sidecar for Maxwell only. This is a design decision for the roadmap, not a blocker for architecture.

**PeerDB caveat:** PeerDB is a managed/self-hosted pipeline tool targeting data warehouses (ClickHouse, Snowflake, BigQuery) as sinks, not Kafka directly. The Docker Compose harness can use PeerDB's Postgres-to-Kafka mirror, but PeerDB's complexity (Temporal orchestrator, internal Postgres catalog, MinIO stage) makes it the heaviest container in the harness. A lightweight alternative: use PeerDB's Postgres-to-Postgres CDC as the tested sink and poll the target Postgres table for event receipt times.

---

## Question 3: Crash Recovery Testing in Docker Compose

**Use `docker compose kill -s SIGKILL <service>` followed by `docker compose start <service>` with health-check polling.**

This is the correct pattern for simulating an abrupt crash (SIGKILL does not allow cleanup — equivalent to `kill -9` or power loss). `docker compose stop` sends SIGTERM, which allows graceful shutdown; that tests graceful restart, not crash recovery.

```bash
# bench/scenarios/crash_recovery.sh (key section)

TOOL="${1:-kaptanto}"   # which tool to crash-test

# 1. Steady load for 10s to establish baseline
start_load --tps 1000 --duration 10s &
LOAD_PID=$!
sleep 10

# 2. Crash the CDC tool with SIGKILL
docker compose -f ../docker-compose.yml kill -s SIGKILL "$TOOL"
CRASH_TIME=$(date +%s%N)   # nanosecond wall clock

# 3. Wait for events to pile up (5 seconds of unprocessed changes)
sleep 5

# 4. Restart the tool
docker compose -f ../docker-compose.yml start "$TOOL"

# 5. Wait for health check to pass
until docker compose -f ../docker-compose.yml ps "$TOOL" \
    --format json | jq -r '.[].Health' | grep -q healthy; do
    sleep 0.5
done
RECOVERY_TIME=$(date +%s%N)

# 6. Signal collector to flush recovery latency
# The collector calculates: time from CRASH_TIME until all in-flight
# events (issued during the 5s dead window) are delivered

kill $LOAD_PID
```

**Key design decisions:**

- Each CDC service in `docker-compose.yml` must have a `healthcheck:` stanza so `docker compose start` can be polled reliably.
- The collector must be resilient to the CDC tool's connection dropping and reconnecting. For SSE (Kaptanto), this means reconnecting with the `Last-Event-ID` header. For Kafka consumers (PeerDB, Maxwell), Kafka handles this automatically via consumer group offsets. For HTTP server adapters (Debezium, Sequin), they must keep the HTTP server running and accept reconnected POSTs.
- Recovery time metric = wall clock time from `docker compose kill` to last undelivered event being received by the collector. This is computed post-hoc in the reporter, not in real-time.

**Docker Compose service health check pattern:**

```yaml
# bench/docker-compose.yml (example for kaptanto)
kaptanto:
  image: kaptanto:bench
  build:
    context: ..
    dockerfile: Dockerfile.bench
  healthcheck:
    test: ["CMD", "curl", "-f", "http://localhost:7700/healthz"]
    interval: 2s
    timeout: 5s
    retries: 10
    start_period: 10s
  depends_on:
    postgres:
      condition: service_healthy
```

---

## Question 4: Metrics Collection and Report Generation Data Flow

**Use a shared `bench/results/` directory mounted as a Docker volume. Two JSONL files: `metrics.jsonl` and `docker_stats.jsonl`. Reporter reads both at run end.**

```
Load Generator                         Collector Binary
    |                                       |
    | INSERT rows with _bench_ts            | Per-tool adapter goroutines
    v                                       | (SSE client / HTTP server / Kafka)
Postgres                                    |
    |                                       | received_at = time.Now().UnixNano()
    | WAL replication                       |
    v                                       v
CDC Tool (kaptanto/debezium/peerdb/...)  MetricEvent{
    |                                       tool, event_id, written_at,
    | event delivery (SSE/HTTP/Kafka)       received_at, table, op
    v                                    }
Adapter in collector binary                 |
    |                                       | append (mutex-protected) to:
    +-------------------------------------> results/metrics.jsonl
                                            (one JSON per line, unbuffered writes)

Docker Stats Scraper goroutine (inside collector)
    |
    | docker stats --no-stream --format json
    | polls every 2 seconds
    v
results/docker_stats.jsonl
    (tool_name, ts, cpu_perc, mem_mib, net_io)

At scenario end:
Reporter binary reads metrics.jsonl + docker_stats.jsonl
    |
    | group by tool, compute p50/p95/p99 latency,
    | throughput (events/sec in 1s windows),
    | peak and mean CPU%, peak and mean RSS
    v
results/report.html  (self-contained: Chart.js bundled inline)
results/report.md    (Markdown table for README embedding)
```

**JSONL format for `metrics.jsonl`:**

```json
{"tool":"kaptanto","event_id":"01J...","written_at":1742000000000000000,"received_at":1742000000012345678,"table":"bench","op":"insert","scenario":"steady"}
```

**JSONL format for `docker_stats.jsonl`:**

```json
{"tool":"kaptanto","ts":1742000000000000000,"cpu_perc":1.23,"mem_mib":42.5,"net_rx_mb":0.01,"net_tx_mb":0.05}
```

**Why JSONL over a database or stdout parsing:**

- JSONL is appendable, inspectable, and trivially parseable by Go without dependencies.
- Stdout parsing is fragile (tool output format changes, buffering issues). Avoid.
- An intermediate database (SQLite) would be appropriate for larger datasets, but for a bench run capped at a few minutes, JSONL at ~200 bytes/event at 1K TPS = ~72 MB for a 5-minute run. Well within memory/disk bounds.
- A shared Docker volume mounts `bench/results/` into both the collector container and the reporter container, so no network file transfer is needed.

**Reporter: self-contained HTML with inline Chart.js:**

The reporter binary generates a single `.html` file with Chart.js source code inlined as a `<script>` block (not a CDN reference). This ensures the report is viewable offline and can be committed to the repo or attached to a GitHub release.

Chart.js 4.x minified is ~200KB. At report generation time, the reporter embeds the minified JS as a Go embed (`//go:embed chartjs/chart.umd.min.js`) and writes it inline.

---

## Question 5: Build Order

**Build order is dictated by runtime dependencies, not code dependencies.**

```
Stage 1 — Infrastructure (no code required)
  docker-compose.yml        define services, networks, volumes
  bench/config/             CDC tool configuration files
  bench/Makefile            orchestration targets

Stage 2 — Load Generator (bench/cmd/loadgen)
  Standalone Go binary: only depends on lib/pq or pgx for Postgres writes.
  No dependency on collector or reporter.
  Build and test independently: can verify load generation without CDC tools running.

Stage 3 — Metrics Collector (bench/cmd/collector)
  Depends on: load generator being defined (knows the event schema, _bench_ts column)
  Depends on: knowledge of each CDC tool's output API (SSE, HTTP push, Kafka)
  Does NOT depend on: reporter
  Contains: per-tool adapters, docker stats scraper, JSONL writer

Stage 4 — Scenarios (bench/scenarios/*.sh)
  Depends on: docker-compose.yml (services), load generator binary, collector binary
  Orchestrates: the full run sequence

Stage 5 — Reporter (bench/cmd/reporter)
  Depends on: JSONL schema being stable (defined by collector in Stage 3)
  Does NOT need a live environment — reads from bench/results/*.jsonl
  Can be developed and tested with fixture JSONL files before running the full harness
  Produces: HTML + Markdown report
```

**Recommended phase sequence for roadmap:**

1. **Phase 11: Harness + Load Generator** — Write `docker-compose.yml` with all services, write `loadgen` binary, verify Postgres receives load. No CDC measurement yet.
2. **Phase 12: Collector + Scenarios** — Write per-tool adapters in `collector`, validate latency measurement logic, wire scenarios.
3. **Phase 13: Reporter** — Write reporter against fixture JSONL, generate HTML report, tune Chart.js charts.

This order is important: the reporter can be built and validated against fake data before the full harness is working. It also allows the Docker Compose configuration to be debugged separately from the Go code.

---

## Recommended Project Structure

```
bench/
├── go.mod                       # module github.com/kaptanto/kaptanto/bench
├── go.sum
├── Makefile                     # up, run, report, clean targets
├── docker-compose.yml           # all services: postgres, kaptanto, debezium, kafka,
│                                #   peerdb, sequin, loadgen, collector
├── Dockerfile.loadgen           # multi-stage: go build ./cmd/loadgen
├── Dockerfile.collector         # multi-stage: go build ./cmd/collector
├── cmd/
│   ├── loadgen/
│   │   └── main.go              # flags: --tps, --duration, --table, --pg-dsn
│   ├── collector/
│   │   ├── main.go              # starts all adapters concurrently, writes JSONL
│   │   ├── adapter.go           # Adapter interface
│   │   ├── kaptanto.go          # SSE client adapter
│   │   ├── debezium.go          # HTTP server adapter (receives POSTs)
│   │   ├── peerdb.go            # Kafka consumer adapter
│   │   ├── maxwell.go           # Kafka consumer adapter (MySQL only -- may be excluded)
│   │   ├── sequin.go            # HTTP server adapter (receives POSTs)
│   │   └── docker_stats.go      # docker stats scraper goroutine
│   └── reporter/
│       ├── main.go              # flags: --metrics, --docker-stats, --out-html, --out-md
│       ├── stats.go             # percentile computation (p50/p95/p99), windowed TPS
│       ├── html.go              # HTML template with inline Chart.js
│       ├── markdown.go          # Markdown table generator
│       └── chartjs/
│           └── chart.umd.min.js # bundled Chart.js 4.x (go:embed target)
├── config/
│   ├── kaptanto.yaml            # kaptanto config (SSE output, bench table)
│   ├── debezium-connector.json  # Connector registration payload
│   └── sequin.yaml              # Sequin CDC source + webhook sink config
├── scenarios/
│   ├── steady.sh                # 30s steady-state at 1K TPS
│   ├── burst.sh                 # 0 → 10K TPS over 10s
│   ├── large_batch.sh           # single INSERT of 50K rows
│   ├── crash_recovery.sh        # SIGKILL + restart, measure recovery
│   └── idle.sh                  # 30s at 0 TPS (baseline resource usage)
└── results/                     # gitignored, written at runtime
    ├── metrics.jsonl
    ├── docker_stats.jsonl
    ├── report.html
    └── report.md
```

### Structure Rationale

- **Separate `bench/go.mod`:** Keeps bench dependencies (Kafka client, HTTP testing utilities) out of the production module. CI treats bench as an independent module.
- **`cmd/` with one directory per binary:** Same convention as `cmd/kaptanto` in the main module. Three focused binaries instead of one monolithic bench binary — easier to test each component in isolation.
- **`config/` as files, not code:** CDC tool configuration (Debezium connector JSON, Sequin YAML) is data, not logic. Volume-mounting these files into containers is the standard pattern for Docker Compose config management.
- **`scenarios/` as shell scripts:** Scenario orchestration involves `docker compose kill`, `sleep`, timing coordination, and signaling between processes. Shell is the right tool. Go subprocesses are not necessary.
- **`results/` gitignored:** Run outputs are ephemeral. If a report needs to be preserved (e.g., committed to the repo as a benchmark artifact), it is copied out explicitly by CI or the user.

---

## Architectural Patterns

### Pattern 1: Timestamp-Embedded Latency Measurement

**What:** Load generator inserts `_bench_ts BIGINT` (nanosecond Unix timestamp) alongside the business row. CDC tool propagates the column as part of the row change event. Consumer reads `_bench_ts` from the event payload and computes `latency_ns = received_at_ns - _bench_ts`.

**When to use:** All throughput and latency scenarios.

**Trade-offs:** Requires a dedicated bench table schema (not production tables). Adds one integer column to every event. Works across all CDC tools because every tool propagates column values. Does not require clock synchronization between the load generator and collector (both run on the same host in Docker Compose).

**Why not use event IDs or Kafka timestamps:** Kafka timestamps have millisecond resolution and include Kafka's internal buffering delay, which muddles the CDC tool's contribution. The `_bench_ts` approach measures the full end-to-end latency from Postgres write to consumer receipt.

### Pattern 2: Sidecar Scraper for Resource Metrics

**What:** A goroutine inside the collector binary polls `docker stats --no-stream --format json` every 2 seconds for each CDC container and appends to `docker_stats.jsonl`. This runs concurrently with event metric collection.

**When to use:** All scenarios (idle scenario particularly depends on this for baseline resource usage).

**Trade-offs:** `docker stats` has ~1% CPU overhead per polled container. At 2-second intervals for 6 containers, this is negligible. Polling interval of 2s gives adequate granularity for a 30-second scenario.

**Alternative rejected:** Docker Engine API (`/containers/{id}/stats?stream=true`) gives streaming stats but requires the Docker socket to be exposed inside the container. Using the CLI from outside Docker Compose is simpler and does not require privileged access.

### Pattern 3: Scenario-as-Signal

**What:** Scenario shell scripts signal the collector to annotate the JSONL stream with scenario boundaries. A simple HTTP endpoint on the collector (`POST /scenario/start`, `POST /scenario/end`) lets the script annotate which scenario each event belongs to.

**When to use:** Running multiple scenarios sequentially in one `bench run`.

**Trade-offs:** Enables the reporter to produce per-scenario charts without separate runs. Adds a small HTTP server to the collector. Simple to implement.

### Pattern 4: Self-Contained Report via Go embed

**What:** The reporter binary uses `//go:embed chartjs/chart.umd.min.js` to bundle Chart.js at compile time. The HTML template writes it inline as `<script>`. The output `.html` file is a single file with no external dependencies.

**When to use:** Report generation only.

**Trade-offs:** The reporter binary is ~200KB larger (Chart.js size). The HTML file opens in any browser without internet. Can be committed to the repo or attached to a GitHub release as a self-contained artifact.

---

## Data Flow

### Steady-State Scenario Flow

```
bench run
    |
    v
Scenario script (steady.sh)
    |
    +--> POST http://collector:8080/scenario/start?name=steady
    |
    +--> loadgen --tps 1000 --duration 30s --pg-dsn postgres://...
    |       |
    |       v
    |   postgres (INSERT bench_events with _bench_ts)
    |       |
    |       | WAL replication (one slot per CDC tool)
    |       |
    |       +---> kaptanto  --SSE--> collector:KaptantoAdapter --> metrics.jsonl
    |       |
    |       +---> debezium  --HTTP POST--> collector:DebeziumAdapter --> metrics.jsonl
    |       |
    |       +---> peerdb    --Kafka--> collector:PeerDBAdapter --> metrics.jsonl
    |       |
    |       +---> sequin    --HTTP POST--> collector:SequinAdapter --> metrics.jsonl
    |
    |   (concurrently)
    |   docker stats scraper (every 2s) --> docker_stats.jsonl
    |
    +--> POST http://collector:8080/scenario/end?name=steady

bench report
    |
    v
reporter reads metrics.jsonl + docker_stats.jsonl
    |
    +--> group events by (tool, scenario)
    +--> compute latency histogram: p50, p95, p99
    +--> compute throughput windows: events/sec per 1s bucket
    +--> compute resource stats: mean CPU%, peak RSS per tool
    |
    v
report.html (Chart.js bar charts: throughput, latency, memory)
report.md   (Markdown comparison table)
```

### Crash Recovery Flow

```
crash_recovery.sh
    |
    +--> start load at 1K TPS for 10s (baseline)
    +--> record CRASH_TIME
    +--> docker compose kill -s SIGKILL <tool>
    +--> continue load for 5s (events pile up in Postgres WAL, unprocessed)
    +--> docker compose start <tool>
    +--> poll healthcheck until healthy
    +--> record RECOVERY_HEALTH_TIME
    +--> wait for collector to receive all events written during dead window
    +--> record LAST_RECOVERED_EVENT_TIME
    |
    v
reporter computes:
    - time_to_health = RECOVERY_HEALTH_TIME - CRASH_TIME
    - time_to_full_recovery = LAST_RECOVERED_EVENT_TIME - CRASH_TIME
    - events_lost (events in Postgres WAL with _bench_ts < CRASH_TIME not received)
```

---

## Integration Points

### New vs Modified Components

| Component | Status | Integration Point |
|-----------|--------|-------------------|
| `bench/docker-compose.yml` | New | Shares Postgres replication slots. Kaptanto built from repo root via `build: context: ..` |
| `bench/cmd/loadgen` | New | Writes to Postgres only. No dependency on kaptanto internals. |
| `bench/cmd/collector` | New | Connects to kaptanto SSE output (`/events` endpoint). Kaptanto code unchanged. |
| `bench/cmd/reporter` | New | Reads JSONL from shared volume. No runtime connection to any service. |
| `cmd/kaptanto` | Not modified | Used as Docker build target. Config passed via `bench/config/kaptanto.yaml` |
| `internal/` packages | Not modified | No changes required. |
| `Makefile` (root) | Modified | Add `bench-up`, `bench-run`, `bench-report`, `bench-clean` targets delegating to `bench/Makefile` |

### External Services in Docker Compose

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| Postgres (shared) | All CDC tools get separate replication slots on the same instance | Use `pg_create_logical_replication_slot` per tool. Each tool must have its own slot name. |
| Kafka (KRaft) | `apache/kafka:3.7` in KRaft mode (no ZooKeeper). PeerDB and Maxwell consume from here. | KRaft simplifies the Compose file: one Kafka container, no ZooKeeper dependency. |
| Debezium Server | `debezium/server:3.x` with `debezium.sink.type=http` | HTTP sink POSTs to collector adapter on host port. |
| PeerDB | `peerdb-io/peerdb` — requires Temporal, internal Postgres catalog, MinIO. Heavy. | Evaluate complexity vs value during Phase 11. May simplify to PeerDB OSS Docker image if available. |
| Sequin | `sequinstream/sequin` with webhook sink configured | Webhook sink POSTs to collector adapter on host port. |
| Kaptanto | Built from repo root via multi-stage Dockerfile | SSE output consumed by collector SSE client. |

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| loadgen -> Postgres | pgx direct connection (not through kaptanto) | Load generator bypasses CDC tools; inserts directly. |
| CDC tools -> Postgres | Each tool's native replication protocol | Each tool registers its own replication slot; slots are independent. |
| CDC tools -> collector | Per-tool protocol (SSE / HTTP push / Kafka) | Collector is the single fan-in point for all metrics. |
| collector -> results/ | File append (JSONL) | Write is append-only; no database; no locking beyond per-line atomic appends. |
| reporter -> results/ | File read (JSONL) | Reporter is read-only; runs after all scenarios complete. |
| scenarios/ -> collector | HTTP signal (POST /scenario/start, /scenario/end) | Lightweight annotation; not in the critical measurement path. |

---

## Anti-Patterns

### Anti-Pattern 1: Single Postgres Replication Slot for All Tools

**What people do:** Register one replication slot and fan out to all CDC tools from a single WAL position.
**Why it's wrong:** CDC tools compete for slot advancement. If one tool crashes, the slot stalls and Postgres WAL accumulates indefinitely (disk exhaustion risk). Also, tools cannot independently control their WAL position.
**Do this instead:** Each CDC tool registers its own named replication slot. Postgres advances them independently. A dead tool's slot stalls only its own progress, not others.

### Anti-Pattern 2: Measuring Latency from Kafka Timestamp

**What people do:** Use Kafka message timestamps (ProduceTime or LogAppendTime) as the reference point for latency measurement.
**Why it's wrong:** Kafka timestamps measure Kafka's internal receipt time, not the original Postgres write time. This masks the CDC tool's WAL-to-Kafka latency and makes comparisons misleading.
**Do this instead:** Embed `_bench_ts` (nanosecond wall clock at Postgres INSERT) in the row. CDC tools propagate it as a column value. Measure latency from `_bench_ts` to `received_at` in the collector.

### Anti-Pattern 3: Running Collector Inside the Same Container as Load Generator

**What people do:** Put load generation and metrics collection in the same process to simplify Docker Compose.
**Why it's wrong:** Load generation is CPU and I/O intensive. Running in the same process as the collector risks stealing CPU from the latency-sensitive measurement path, inflating measured latencies.
**Do this instead:** Separate binaries in separate containers. Load generator container and collector container run independently. Docker Compose resource limits can be set on the load generator to prevent it from starving the measurement path.

### Anti-Pattern 4: Parsing Stdout of CDC Tools

**What people do:** Capture `docker logs` or stdout of each CDC tool and parse log lines for event data.
**Why it's wrong:** Log format is not a stable API. Debezium, Sequin, and PeerDB all use structured logging with formats that change between versions. Parsing logs conflates observability output with data delivery.
**Do this instead:** Use each tool's native output mechanism (HTTP push, Kafka, SSE). These are stable, versioned APIs.

### Anti-Pattern 5: Shared Metrics File Written Concurrently Without Coordination

**What people do:** Have all five adapter goroutines write directly to `metrics.jsonl` concurrently with `os.File.Write`.
**Why it's wrong:** `os.File.Write` is not atomic for multi-goroutine concurrent writers on all platforms. Two goroutines can interleave their writes mid-line, corrupting JSONL.
**Do this instead:** Fan all MetricEvents through a single goroutine that owns the file descriptor. Adapters send to a shared `chan MetricEvent`; the writer goroutine drains the channel and appends one line at a time.

---

## Scaling Considerations

The benchmark suite is a development tool, not a production service. Scaling concerns are about benchmark validity rather than user load.

| Concern | Implication | Mitigation |
|---------|-------------|------------|
| Load generator throughput | Must outrun all CDC tools combined, or it becomes the bottleneck | `loadgen` uses batch INSERTs (COPY or multi-row VALUES). Target: 10K TPS per run. |
| Collector CPU at high TPS | At 1K TPS x 5 tools = 5K events/sec through the collector | Go goroutine per adapter is sufficient. Channel buffer of 10K events handles bursts. |
| Docker Compose startup order | Postgres must be healthy before CDC tools start replication | Use `depends_on: condition: service_healthy` throughout |
| Replication slot accumulation | Dead or slow tools accumulate WAL on Postgres | Set `max_slot_wal_keep_size = 1GB` in Postgres config; monitor via pg_replication_slots |
| Kafka KRaft startup time | KRaft mode can take 15-30s to elect a controller on first start | Add `start_period: 30s` to Kafka healthcheck |

---

## Sources

- [Docker Compose kill documentation](https://docs.docker.com/reference/cli/docker/compose/kill/) — HIGH confidence (official)
- [Docker container stats JSON format](https://docs.docker.com/reference/cli/docker/container/stats/) — HIGH confidence (official)
- [Debezium Server HTTP sink configuration](https://debezium.io/documentation/reference/stable/operations/debezium-server.html) — HIGH confidence (official)
- [Maxwell's Daemon producers](https://maxwells-daemon.io/producers/) — HIGH confidence (official); Maxwell only supports MySQL, not Postgres
- [Sequin CDC performance and delivery](https://sequinstream.com/docs/performance) — MEDIUM confidence (official docs, but benchmark claims are marketing)
- [PeerDB GitHub architecture](https://github.com/PeerDB-io/peerdb) — MEDIUM confidence (GitHub README, reviewed 2026-03-20)
- [Kafka KRaft mode Docker Compose](https://github.com/zanty2908/cdc-debezium-kafka) — MEDIUM confidence (community example, not official)
- [Chart.js embedding](https://www.chartjs.org/) — HIGH confidence (official, standard pattern)
- [Go embed directive](https://pkg.go.dev/embed) — HIGH confidence (Go stdlib, official)

---

*Architecture research for: Kaptanto v1.2 Benchmark Suite (bench/ directory)*
*Researched: 2026-03-20*
