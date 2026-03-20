# Project Research Summary

**Project:** Kaptanto v1.2 — CDC Benchmark Suite
**Domain:** Comparative CDC benchmarking harness (Postgres WAL tools)
**Researched:** 2026-03-20
**Confidence:** HIGH

## Executive Summary

Kaptanto v1.2 adds a self-contained benchmark suite to the existing Go binary. The suite lives in a `bench/` subdirectory with its own Go module, orchestrates four CDC tools (Kaptanto, Debezium, PeerDB, Sequin) against a shared Postgres instance via Docker Compose, and produces a self-contained HTML report with an accompanying Markdown summary. Every tool runs in Docker; no cloud infrastructure or external dependencies are needed. The recommended architecture uses three focused Go binaries — a load generator, a metrics collector, and a report generator — plus per-tool adapter logic inside the collector to accommodate each tool's native output protocol (SSE, HTTP push, Kafka).

The primary technical risks are benchmark validity, not implementation complexity. Eleven concrete pitfalls were identified: JVM warm-up bias in Debezium and PeerDB, Postgres WAL slot interference when all tools run simultaneously, inaccurate memory measurement under cgroups v2, clock skew in latency measurement, Debezium's Kafka overhead being conflated with its CDC-engine latency, and community credibility loss if the methodology is not fully reproducible. Every pitfall has a known mitigation strategy, and all must be locked into the harness design in Phase 11 before any measurement code is written. The build order is strictly linear — Docker Compose harness first, then load generator, then collector and scenarios, then reporter — because the JSONL schema produced by the collector gates the reporter's implementation.

Maxwell's Daemon must be excluded from the benchmark. It supports only MySQL and has no Postgres WAL support (confirmed by GitHub issue #434, closed 2023). The four-tool comparison (Kaptanto, Debezium Server, Sequin, PeerDB) is defensible on its own and should be treated as the baseline scope. Estuary Flow is a candidate replacement as a fifth tool but requires validation at phase planning time.

## Key Findings

### Recommended Stack

The benchmark suite reuses Kaptanto's existing `jackc/pgx/v5` dependency for the load generator and adds three new Go libraries: `github.com/docker/docker` v28.x for container metrics via the Docker Engine API (`ContainerStatsOneShot`), `github.com/HdrHistogram/hdrhistogram-go` v1.2.0 for O(1) latency percentile recording at high event rates, and `github.com/go-echarts/go-echarts/v2` v2.7.1 for self-contained HTML chart generation with ECharts JS bundled inline. Report scaffolding uses stdlib `html/template` and `text/template`; no report-generation library adds value over stdlib. The harness drives Docker Compose via `os/exec` against the `docker compose` CLI.

Debezium must be pulled from `quay.io/debezium/server:3.4` — Docker Hub images for Debezium 3.x are no longer published. PeerDB requires six containers (Temporal, flow-api, flow-worker, flow-snapshot-worker, catalog-postgres, MinIO); this operational footprint is part of what the benchmark measures and must be represented in full. Sequin requires two containers (sequin, redis). The total Docker Compose service count is approximately 13 containers, and Docker resource limits must be set carefully.

**Core technologies:**
- `jackc/pgx/v5` v5.8.0: load generator INSERT workload — already in go.mod, zero new dependency
- `github.com/docker/docker` v28.x: per-container CPU% and RSS via Docker Engine API — `ContainerStatsOneShot` preferred over streaming to avoid warm-up delay artifacts
- `github.com/HdrHistogram/hdrhistogram-go` v1.2.0: p50/p95/p99 latency histograms — O(1) recording essential at 500K+ events/sec where `sort.Slice` is impractical
- `github.com/go-echarts/go-echarts/v2` v2.7.1: self-contained HTML report with inline ECharts JS — offline-viewable, no CDN, interactive zoom
- `stdlib text/template + html/template`: Markdown summary and HTML scaffolding — no library adds value over stdlib

### Expected Features

**Must have (table stakes):**
- Single-command launch (`docker compose up` / `make bench-run`) — no benchmark is credible without this
- Steady-state throughput scenario (60s at 10k ops/s default, configurable `--rate` flag)
- End-to-end latency p50/p95/p99 using `clock_timestamp()` embedded in payload — `now()` is the wrong choice because it returns transaction start time, not per-row wall clock
- CPU% and RSS per tool including all required infrastructure containers (Redis for Sequin, Temporal + MinIO for PeerDB)
- Idle resource scenario (60s at zero load) — Debezium JVM idle footprint is a primary Kaptanto differentiator
- Crash and recovery scenario (SIGKILL + restart, measure time-to-resume and event gap detection)
- Pinned Docker image versions for all tools — reproducibility is a hard requirement
- Self-contained HTML report (ECharts JS inlined) with Markdown summary table auto-generated from same data
- Methodology section embedded in HTML report: full configuration per tool, measurement approach, known limitations, Docker latency floor
- Mixed workload: 60% INSERT, 30% UPDATE, 10% DELETE
- Warm-up period excluded from all measurements (minimum 60s for JVM tools, 10s for Go tools)

**Should have (competitive differentiators):**
- Burst/spike scenario (0 → 5x normal rate × 3 cycles) — surfaces head-of-line blocking and backpressure failure modes
- Large transaction scenario (500K row single INSERT) — exposes Debezium in-memory transaction buffering
- Row size variants (100B vs 1KB) — surfaces row-width scaling differences
- Per-scenario JSON data files committed alongside HTML report — enables community verification and re-running
- Event correctness verification post-recovery (gapless event stream check against Postgres ground-truth counter)
- Open-source config files per tool committed in `bench/config/` (not prose descriptions)

**Defer (v2+):**
- Backfill/snapshot scenario — each tool has a different backfill architecture; fair comparison requires deep per-tool analysis not scoped to v1.2
- EC2/cloud variant — high reproducibility cost, introduces network jitter as uncontrolled variable
- Maxwell's Daemon — only if Maxwell adds credible Postgres support in the future
- Live dashboard with WebSocket streaming — complexity with no credibility benefit over static report

### Architecture Approach

The benchmark suite is a fully independent sub-system under `bench/` with its own `go.mod` (module `github.com/kaptanto/kaptanto/bench`). It never modifies `cmd/kaptanto` or `internal/`. The three binaries (`bench/cmd/loadgen`, `bench/cmd/collector`, `bench/cmd/reporter`) are independently buildable and testable. The collector embeds per-tool adapter goroutines that each speak the tool's native protocol: SSE client for Kaptanto, HTTP servers for Debezium and Sequin (push model), and a Kafka consumer for PeerDB. All adapters fan events through a single `chan MetricEvent` to a file-owning goroutine that appends JSONL — concurrent file writes are explicitly prohibited (JSONL interleaving). Shell scripts in `bench/scenarios/` orchestrate timing because subprocess coordination, `docker compose kill`, and signal handling are simpler in bash than Go subprocess management.

**Major components:**
1. `bench/cmd/loadgen` — inserts rows at configurable rate with `_bench_ts BIGINT` (nanosecond wall clock) embedded for latency attribution; mixed INSERT/UPDATE/DELETE at 60/30/10 ratio; uses pgx COPY/batch protocol
2. `bench/cmd/collector` — per-tool adapter goroutines (SSE client, HTTP server ×2, Kafka consumer), docker stats scraper goroutine, scenario annotation HTTP endpoint (`POST /scenario/start|end`), single JSONL writer goroutine; produces `metrics.jsonl` and `docker_stats.jsonl`
3. `bench/cmd/reporter` — reads JSONL, computes p50/p95/p99 via HdrHistogram, windowed throughput series, resource stats; renders self-contained HTML via go-echarts and Markdown via text/template; can be developed and tested against fixture JSONL before the full harness is working
4. `bench/docker-compose.yml` — all 13 services with healthchecks, `cpuset` pinning per tool, `depends_on: condition: service_healthy` throughout, `max_slot_wal_keep_size = 1GB` on Postgres
5. `bench/scenarios/*.sh` — sequential scenario orchestrators; drive loadgen, signal collector, execute SIGKILL for crash recovery, poll healthcheck for recovery detection

### Critical Pitfalls

1. **JVM cold-start bias (Debezium + PeerDB)** — JVM tools need 60+ seconds to reach steady state after `service_healthy` reports healthy. Build a mandatory warm-up phase that sends and discards 100K events before opening the measurement window. Never begin recording until throughput variance over a 10-second window is below 10%.

2. **Debezium Kafka overhead conflated with CDC-engine latency** — Use Debezium Server (HTTP sink, standalone, no Kafka) as the primary comparison configuration. If Kafka mode is tested, label it separately and document that Kafka `linger.ms`/batching accounts for the latency difference, not Debezium's WAL reader.

3. **Shared Postgres WAL slot interference** — A lagging slot forces Postgres to retain WAL, increasing I/O for all tools and corrupting measurements. Set `max_slot_wal_keep_size = 1GB`, monitor `pg_replication_slots.confirmed_flush_lsn` every 5 seconds, and abort the scenario if any slot falls more than 100MB behind. Support a sequential run mode (one tool at a time) for isolated per-tool measurements.

4. **cgroups v2 memory inflation** — `docker stats` on Linux with cgroups v2 (Ubuntu 22.04+) includes page cache in RSS, inflating mmap-heavy tools like Kaptanto (Badger) and Debezium (off-heap buffers) by 5-10x. Use `/proc/1/status` `VmRSS` via sidecar exec or Prometheus `process_resident_memory_bytes` for authoritative RSS.

5. **Non-reproducible results invite community backlash** — Every benchmark that publishes results without raw data, exact configuration files, and runnable harness code gets dismissed. Publish all harness code under `bench/` as open source. Commit raw JSON alongside the HTML report. Include at least one scenario where a competitor outperforms Kaptanto to demonstrate objectivity.

6. **Maxwell's Daemon is MySQL-only** — Maxwell has no Postgres WAL support (GitHub issue #434, closed 2023). Exclude it from all Postgres scenarios. Add an explicit note in the report. Do not include Maxwell in the Docker Compose file unless a separate MySQL instance is also provided and all Maxwell results are labeled "MySQL source."

## Implications for Roadmap

Based on the build-order dependency chain in ARCHITECTURE.md, three phases are the natural structure:

### Phase 11: Harness + Load Generator
**Rationale:** Everything else depends on a working Docker Compose environment and a load generator that inserts rows with `_bench_ts`. Pitfall-prevention decisions that cannot be changed later (cpuset, Debezium Server vs. Kafka mode, Maxwell exclusion, PeerDB container grouping for resource accounting, Docker network mode, slot monitoring) must be locked here before any measurement code is written.
**Delivers:** `bench/docker-compose.yml` with all tool services and healthchecks; `bench/cmd/loadgen` binary; verified Postgres receiving load at target TPS; all tool configurations committed in `bench/config/`; slot lag monitoring scaffolded
**Addresses features:** single-command launch, pinned versions, shared Postgres with independent replication slots, mixed workload schema, `REPLICA IDENTITY FULL` on bench tables
**Avoids pitfalls:** Debezium mode decision locked; Maxwell excluded with report note; PeerDB full container group defined for resource accounting; `cpuset` pinned to prevent CPU contention; bridge network consistency enforced; slot interference mitigation in place

### Phase 12: Collector + Scenarios
**Rationale:** The collector is the most complex component — five per-tool adapters with different protocols, concurrent goroutines, and a JSONL schema that gates the reporter. Scenarios depend on the collector being ready to annotate scenario boundaries. Locking the JSONL schema here before Phase 13 avoids rework.
**Delivers:** `bench/cmd/collector` with all adapters; `bench/scenarios/*.sh` (steady, burst, large_batch, crash_recovery, idle); verified JSONL output from a real run; docker stats scraper producing `docker_stats.jsonl`
**Uses:** `github.com/docker/docker` v28.x, `github.com/HdrHistogram/hdrhistogram-go` v1.2.0, `kafka-go` for PeerDB adapter
**Addresses features:** latency measurement via `_bench_ts` + `clock_timestamp()`, CPU%/RSS collection, crash recovery SIGKILL, warm-up phase, idle scenario, burst scenario, mixed workload
**Avoids pitfalls:** cgroups v2 RSS via `/proc/1/status`; per-tool crash recovery specification (which containers to kill); Sequin Redis and PeerDB Temporal included in resource accounting; latency measurement uses `clock_timestamp()` not `now()`

### Phase 13: Reporter
**Rationale:** The reporter has no live runtime dependency — it reads JSONL files. It can be developed and validated against fixture JSONL before the full harness is stable, enabling Phase 13 to proceed in parallel with Phase 12 stabilization if schedule permits.
**Delivers:** `bench/cmd/reporter` producing self-contained HTML + Markdown; methodology section embedded in report; raw JSON bundled; `bench/README.md` with reproduction steps; publication checklist verified
**Uses:** `github.com/go-echarts/go-echarts/v2` v2.7.1, stdlib `html/template`, `text/template`, `embed`
**Addresses features:** interactive HTML charts (throughput time series, latency percentiles, CPU/RSS comparison, recovery table), Markdown summary for README embedding, per-scenario data files, methodology section with full tool configurations
**Avoids pitfalls:** zero-baseline y-axes on all bar charts; raw data linked from every chart; reproducibility verified on clean checkout; at least one scenario where a competitor outperforms Kaptanto included

### Phase Ordering Rationale

- The JSONL schema is the critical interface between Phase 12 (collector) and Phase 13 (reporter). Defining it in Phase 12 allows Phase 13 to work from stable fixtures independently.
- Phase 11 must lock all pitfall-prevention decisions before any measurement code is written. Changing Debezium mode, cpuset, or PeerDB resource accounting after the collector is built requires rebuilding instrumentation and invalidating prior runs.
- The load generator (`_bench_ts` column, mixed workload, `clock_timestamp()`) must exist before the collector can be tested end-to-end; separating them into distinct phases keeps each deliverable independently verifiable.
- The reporter is the only phase that can start before the harness produces real data — its pure function over JSONL makes it the natural parallelism opportunity.

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 11 (PeerDB replacement / Maxwell):** Estuary Flow is the recommended Maxwell replacement but requires validation — Docker image availability, Postgres WAL support, HTTP/SSE sink availability. If Estuary Flow is not viable, four-tool comparison is the fallback and no additional research is needed.
- **Phase 11 (Kafka KRaft for PeerDB):** KRaft configuration in `apache/kafka:3.7` for PeerDB output needs a working compose example validated against the current PeerDB OSS stack.
- **Phase 12 (PeerDB adapter protocol):** PeerDB's native output path in the benchmark context needs confirmation. The collector design assumes a Kafka mirror, but verify whether PeerDB OSS's current Docker Compose exposes a usable Kafka-based output or whether an alternative sink is required.

Phases with well-documented patterns (skip research-phase):
- **Phase 11 (load generator):** pgx batch INSERT pattern is standard and documented; `bench_events` schema is defined in STACK.md.
- **Phase 12 (Kaptanto SSE adapter):** Kaptanto's own SSE output is the home-team code; no external research needed.
- **Phase 12 (Debezium HTTP sink adapter):** Debezium Server HTTP sink is official and well-documented; Go HTTP server adapter is straightforward.
- **Phase 12 (docker stats scraper):** Docker Engine API `ContainerStatsOneShot` is documented and verified at v28.5.2.
- **Phase 13:** go-echarts v2.7.1 is current and well-documented; report generation is a pure function of the JSONL schema.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All libraries verified at pkg.go.dev with explicit versions. Docker image sources (quay.io for Debezium, ghcr.io for PeerDB/Sequin) confirmed from official repos and release announcements. |
| Features | MEDIUM | Core methodology (clock_timestamp latency, mixed workload, docker stats) verified against Sequin, Debezium, and db-benchmarks patterns. Load numbers (10k ops/s on Docker) validated from Sequin published benchmarks. PeerDB-specific throughput claims are LOW confidence as no public methodology exists. |
| Architecture | HIGH | Three-binary structure, JSONL data flow, adapter interface pattern, scenario-as-signal, and crash recovery SIGKILL + healthcheck polling are all established patterns with no novel elements. |
| Pitfalls | HIGH | Eleven pitfalls identified; eight have HIGH-confidence sources (official Debezium docs, Gunnar Morling WAL slot analysis, Oracle JVM benchmarking guide, cgroups v2 confirmed behavior from multiple sources). Three have MEDIUM-confidence sources. |

**Overall confidence:** HIGH

### Gaps to Address

- **Maxwell replacement:** Estuary Flow is recommended but unvalidated. Phase 11 planning should confirm Docker image, Postgres WAL support, and compatible output sink. Fallback is four-tool comparison with no fifth tool; this is fully defensible.
- **PeerDB Kafka adapter:** PeerDB's CDC output path needs confirmation. Verify the exact Kafka configuration required before writing the PeerDB adapter in Phase 12. PeerDB's docker-compose.yml evolves rapidly.
- **cgroups version on target CI host:** Memory measurement approach (`/proc/1/status` vs `docker stats`) depends on the host cgroup version. Confirm target environment (Linux kernel, Ubuntu version) before Phase 12 to avoid post-publication measurement disputes.
- **Debezium Server vs. Kafka mode scope:** FEATURES.md and PITFALLS.md both flag this. The resolution — Debezium Server as primary, Kafka mode as optional labeled secondary — must be committed in Phase 11 before any Debezium configuration work begins.

## Sources

### Primary (HIGH confidence)
- [Debezium Server docs](https://debezium.io/documentation/reference/stable/operations/debezium-server.html) — standalone operation, HTTP sink, Postgres connector
- [Debezium quay.io migration notice](https://debezium.io/blog/2024/09/18/quay-io-reminder/) — Docker Hub images removed; quay.io is the correct registry
- [Debezium 3.4.0.Final release](https://debezium.io/blog/2025/12/16/debezium-3-4-final-released/) — latest stable version
- [PeerDB docker-compose.yml](https://github.com/PeerDB-io/peerdb/blob/main/docker-compose.yml) — Temporal + MinIO + 3 flow containers confirmed
- [Maxwell issue #434](https://github.com/zendesk/maxwell/issues/434) — no Postgres support, maintainer confirmed, closed 2023
- [Sequin docker-compose.yaml](https://github.com/sequinstream/sequin/blob/main/docker/docker-compose.yaml) — Redis dependency confirmed
- [pkg.go.dev/github.com/go-echarts/go-echarts/v2](https://pkg.go.dev/github.com/go-echarts/go-echarts/v2) — v2.7.1, Mar 14, 2026
- [pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go](https://pkg.go.dev/github.com/HdrHistogram/hdrhistogram-go) — v1.2.0, Nov 2025
- [pkg.go.dev/github.com/docker/docker/client](https://pkg.go.dev/github.com/docker/docker/client) — v28.5.2, ContainerStatsOneShot confirmed
- [Measuring Debezium Server performance (Debezium official, Feb 2026)](https://debezium.io/blog/2026/02/02/measuring-debezium-server-performance-mysql-streaming/) — YCSB methodology, JVM warm-up behavior
- [Mastering Postgres Replication Slots (Gunnar Morling)](https://www.morling.dev/blog/mastering-postgres-replication-slots/) — WAL slot interference between multiple CDC consumers
- [RSS memory data in docker stats api for cgroup2 (RealWorldAI)](https://realworldai.co.uk/post/rss-memory-data-in-docker-stats-api-for-cgroup2) — cgroups v2 memory inflation behavior confirmed
- [docker tasks with cgroups v2 report combined RSS + cache (Hashicorp/Nomad #16230)](https://github.com/hashicorp/nomad/issues/16230) — cgroups v2 RSS inflation corroborated
- [Avoiding Benchmarking Pitfalls on the JVM (Oracle)](https://www.oracle.com/technical-resources/articles/java/architect-benchmarking.html) — JVM warm-up and deoptimization mechanics
- [Overcoming Pitfalls of Postgres Logical Decoding (PeerDB blog)](https://blog.peerdb.io/overcoming-pitfalls-of-postgres-logical-decoding) — REPLICA IDENTITY FULL requirement for fair comparison
- [Fair Benchmarking Considered Difficult — Raasveldt et al. (DBTEST 2018)](https://mytherin.github.io/papers/2018-dbtest.pdf) — benchmark methodology pitfalls, configuration sensitivity factor of 28x

### Secondary (MEDIUM confidence)
- [Sequin Performance Docs](https://sequinstream.com/docs/performance) — 60k ops/s ceiling, `updated_at` timestamp methodology
- [Benchmarking CDC Tools: Supermetal vs Debezium vs Flink CDC (Streamingdata.tech)](https://www.streamingdata.tech/p/benchmarking-cdc-tools) — Kafka producer tuning doubles Debezium throughput
- [db-benchmarks Framework](https://db-benchmarks.com/framework/) — Docker Compose single-command pattern, cold/warm run discipline
- [Debezium CDC Pain Points (Estuary)](https://estuary.dev/blog/debezium-cdc-pain-points) — JVM resource requirements 4-8 GB RAM in production
- [PeerDB GitHub architecture](https://github.com/PeerDB-io/peerdb) — container architecture reviewed 2026-03-20
- [Docker Networking Modes Performance Comparison (Dec 2025)](https://eastondev.com/blog/en/posts/dev/20251217-docker-network-modes/) — bridge vs. host latency overhead figures
- [Benchmarking Postgres Replication: PeerDB vs Airbyte (PeerDB blog)](https://blog.peerdb.io/benchmarking-postgres-replication-peerdb-vs-airbyte) — parallelism=1 for fair comparison methodology

### Tertiary (LOW confidence — needs validation)
- [PeerDB Why PeerDB Docs](https://docs.peerdb.io/why-peerdb) — 5-60s lag claims have no public methodology; treat as marketing claims only

---
*Research completed: 2026-03-20*
*Ready for roadmap: yes*
