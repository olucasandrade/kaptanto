# Feature Research

**Domain:** CDC Benchmarking Suite (comparison tool for Kaptanto v1.2)
**Researched:** 2026-03-20
**Confidence:** MEDIUM — core benchmark methodology well-established from Sequin, Debezium, and database benchmarking literature; specific load numbers and latency measurement approaches verified from multiple sources; tool-specific details (Maxwell Postgres support, PeerDB setup complexity) LOW confidence

---

## Feature Landscape

### Table Stakes (Users Expect These)

Features evaluators assume exist. Missing these = the benchmark is not credible.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Single-command launch | Every credible open benchmark (TechEmpower, db-benchmarks, ClickBench) runs with one command | LOW | `docker compose up` pattern; all services spin together including Postgres, all CDC tools, load generator, consumer |
| Steady-state throughput scenario | Core CDC metric — sustained ops/sec over time | MEDIUM | 60-120 second measurement window after warmup; Sequin benchmarks at up to 60k ops/s; 10k–50k is realistic for commodity Docker |
| End-to-end latency (p50/p95/p99) | Vendors claim sub-second latency; evaluators need independent verification | HIGH | Must embed `clock_timestamp()` in payload at write time; not measured consumer-side alone — see Latency Methodology section |
| CPU% and RSS memory collection per tool | Debezium JVM vs Go binary is a primary evaluation axis; Debezium needs 4–8 GB RAM in production | LOW | `docker stats` scrape per container at 1s intervals; collect for all tools including Postgres |
| Idle resource usage scenario | JVM tools (Debezium) have large footprint at zero load; Go/Rust tools do not; this is a compelling story for Kaptanto | LOW | Spin up all tools, let settle 30s with zero writes, collect 60s of CPU+RSS |
| Crash and recovery scenario | CDC tools are infrastructure; correctness under restart is non-negotiable for evaluators | HIGH | Kill CDC tool container mid-load, restart, measure time-to-resume and whether events are missed or duplicated |
| Reproducible, pinned tool versions | Critical for credibility; Docker image tags must be explicit, never `latest` | LOW | Pin Debezium, Sequin, PeerDB versions in compose file; document in methodology section of report |
| HTML report with charts | Evaluators share reports; a raw CSV is not shareable | MEDIUM | Self-contained single HTML file (Chart.js inlined, no CDN dependency); opens in any browser |
| Markdown summary | Embedding results in a GitHub README is standard community practice | LOW | Auto-generated from same JSON data that drives HTML; table with one headline number per tool per scenario |
| Burst / spike scenario | Steady-state does not surface head-of-line blocking or backpressure failure modes | MEDIUM | 10s of zero writes, then 5x normal rate for 10s, repeat 3 times; measure latency spikes during ramp |
| Warmup period excluded from measurement | Including JVM JIT warmup in results biases against JVM tools for short tests | LOW | 30s warmup runs not recorded; 60s measurement window; call out explicitly in methodology notes embedded in report |

### Differentiators (Competitive Advantage)

Features that make this benchmark more authoritative than existing vendor benchmarks.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| All tools share the same Postgres instance | Eliminates "your Postgres vs mine" as a confounder; single source, all CDC tools consume independently | MEDIUM | Each tool gets its own replication slot; Postgres WAL retained until all slots confirm; WAL slot count is the constraint |
| Large transaction / bulk insert scenario | Debezium buffers whole transactions in memory before emitting; large transactions expose this; most vendor benchmarks avoid it | MEDIUM | Single `INSERT INTO ... SELECT` of 500k rows; measure time-to-first-event and time-to-last-event per tool |
| Mixed workload (INSERT + UPDATE + DELETE) | Pure INSERT benchmarks favor tools with append-only optimizations; real OLTP workloads are mixed | LOW | Load generator sends 60% INSERT, 30% UPDATE, 10% DELETE; Sequin uses this distribution in their own benchmarks |
| Per-scenario JSON data file | Enables community verification, re-running specific scenarios, and building on results over time | LOW | One JSON file per scenario per run; HTML report reads from bundled JSON; commit result files to repo |
| Row size variants | Throughput in events/sec behaves differently at 100B vs 1KB rows; Sequin tests 4 row sizes; no other public benchmark covers this | MEDIUM | Test two sizes: small (100B) and large (1KB); surface if any tool degrades on wide rows or large payloads |
| Open methodology documentation | Vendor benchmarks omit configuration details; the #1 community criticism is "what settings did you use?" | LOW | Embed full tool configuration (Debezium connector JSON, PeerDB env vars, Kaptanto YAML, Sequin config) in the HTML report as a collapsible section |
| Crash recovery correctness check | No existing public CDC benchmark tests correctness after restart; most only measure throughput | HIGH | After restart, verify that the event stream is gapless (no skipped events) by comparing against a ground-truth counter written to Postgres during the load |

### Anti-Features (Commonly Requested, Often Problematic)

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| Cloud / EC2 harness | "More realistic" hardware | Introduces network jitter, instance variability, and cost barrier; Debezium's own EC2-based benchmark is why nobody runs it — it requires Terraform + AWS credentials | Local Docker Compose; document the host hardware requirements (16 GB RAM recommended); note expected scaling behavior at the bottom of the report |
| Kafka as measurement sink | "Production-grade" destination; Debezium requires Kafka | Adds Kafka as an uncontrolled variable; Debezium+Kafka measures Kafka throughput, not Debezium's CDC pipeline; Sequin-vs-Debezium uses Kafka but it unfairly penalizes Kafka-independent tools like Kaptanto | Use a minimal consumer binary per tool that reads from each tool's native output (stdout/SSE for Kaptanto, HTTP for Sequin, Kafka for Debezium using a local broker) and writes timestamped receipts to SQLite |
| Automated performance regression CI gate | "Catch regressions automatically" | Docker-on-CI has high variance; timer-based assertions produce flaky failures; benchmark runs are not deterministic enough for gating | Run benchmarks on demand, not in CI; commit result JSON to repo; compare across versions by diffing committed result files |
| Measuring Postgres WAL write throughput as a CDC metric | "Complete picture of pipeline" | Postgres throughput is shared across all CDC tools since they share the same instance; it measures the load generator ceiling, not the CDC tools | Report WAL throughput as a reference ceiling only; each tool's consumer lag is the CDC-specific metric |
| Sub-millisecond latency claims | "Real-time" marketing appeal | Docker networking adds 0.5–2ms baseline; sub-ms claims on Docker are meaningless and invite backlash from the technical community; PeerDB's "5-60 second lag" claims were criticized for having no disclosed methodology | Report p50/p95/p99 with an explicit note that Docker adds 1-5ms base latency; state the measurement floor clearly |
| Snapshot / backfill performance scenario | Relevant for migrations | Each tool has a different backfill architecture (Kaptanto keyset cursor, Debezium full-table lock, PeerDB parallel chunks) — not comparable without deep per-tool tuning that would take longer than the benchmark itself to validate | Exclude from v1.2 with a note explaining why; mark as a future benchmark scenario |
| Real-time streaming result updates (live dashboard) | "More impressive demo" | Adds WebSocket server, browser polling, significant implementation complexity for no credibility benefit | Static HTML report generated after run completes; simplicity is a trust signal |

---

## Feature Dependencies

```
[Shared Postgres Container]
    └──required-by──> [All CDC Tool Containers]
                          └──required-by──> [All Scenario Runners]
                                                └──required-by──> [Report Generator]

[Load Generator Container]
    └──drives──> [Steady-State Scenario]
    └──drives──> [Burst Scenario]
    └──drives──> [Large Transaction Scenario]
    └──embeds──> [clock_timestamp() in row payload]

[clock_timestamp() in row payload]
    └──required-by──> [End-to-End Latency Measurement]

[Consumer Binary (one per CDC tool)]
    └──reads-from──> [CDC Tool Output (stdout/SSE/HTTP/Kafka)]
    └──writes-to──> [Per-Scenario JSON Result File]
    └──required-by──> [Crash Recovery Correctness Check]

[Per-Scenario JSON Result File]
    └──required-by──> [HTML Report Generator]
    └──required-by──> [Markdown Summary Generator]

[Crash Recovery Scenario]
    └──requires──> [Consumer Binary] (to detect event gap)
    └──requires──> [Load Generator] (must keep writing during kill/restart)
    └──requires──> [Ground-Truth Counter in Postgres] (to verify no events skipped)

[docker stats scraper]
    └──writes-to──> [Per-Scenario JSON Result File]
    └──runs-during──> [All Scenarios including Idle]
```

### Dependency Notes

- **clock_timestamp() requires load generator cooperation.** The load generator must write a `benchmark_ts` column using `clock_timestamp()`, not `now()`. The difference: `now()` returns the transaction start time (can be milliseconds stale in a long transaction); `clock_timestamp()` returns the actual wall-clock time at the moment the row is inserted. This gives sub-transaction precision for latency measurement.
- **Consumer binary is the critical new component.** Each CDC tool emits events in a different format to a different sink: Kaptanto uses stdout/SSE/gRPC, Sequin uses HTTP, Debezium uses Kafka topics. A consumer adapter per tool (or a configurable single consumer) must read events, extract the embedded `benchmark_ts`, compute latency, and write samples to a shared result store. This is MEDIUM complexity — the hardest part of the benchmark infrastructure.
- **Crash recovery requires the load generator to keep running.** The scenario kills the CDC tool container mid-load while the load generator continues writing to Postgres. The consumer must detect the gap in the event stream and measure time from kill to when the last pre-kill event is confirmed delivered post-restart.
- **Idle scenario requires all scenarios to share a stable docker-compose.yml.** Do not spin up/down containers per scenario — start everything once, run scenarios sequentially. This avoids JVM cold-start contaminating throughput numbers.
- **HTML report is downstream of all scenario JSON files.** Generate in a final `generate-report` step after all scenarios complete. Single-pass template render; no server required.

---

## MVP Definition

### Launch With (v1.2)

Minimum viable benchmark that can be linked from the Kaptanto README and withstand community scrutiny.

- [ ] Docker Compose harness: Postgres + Kaptanto + Debezium + Sequin + PeerDB; Maxwell excluded (MySQL-only, no credible Postgres support) with a note in the report explaining the exclusion
- [ ] Load generator container: mixed INSERT/UPDATE/DELETE (60/30/10), configurable ops/sec (default 10k/s), embeds `clock_timestamp()` in a `benchmark_ts` column
- [ ] Consumer adapter per tool: reads events from each tool's native output, extracts `benchmark_ts`, records `(tool, scenario, latency_ms, event_count, ts)` to SQLite
- [ ] docker stats scraper: polls CPU% and RSS per container at 1s intervals during each scenario, writes to SQLite
- [ ] Scenarios implemented: steady-state (60s at 10k ops/s), burst (3x spike × 3 cycles), idle (60s zero load), crash+recovery (kill mid-run, restart)
- [ ] Per-scenario JSON result files bundled with report
- [ ] Self-contained HTML report (Chart.js inlined, no CDN, single file): bar charts for throughput, line charts for latency percentiles over time, table for CPU/RSS comparison, recovery time table
- [ ] Markdown summary table auto-generated alongside HTML (for README embedding)
- [ ] Pinned Docker image versions for all tools in compose file
- [ ] Methodology section embedded in HTML report: configuration used per tool, measurement approach, known limitations, Docker latency floor

### Add After Validation (v1.x)

- [ ] Large transaction scenario (500k row bulk insert) — add once v1.2 is stable; exposes Debezium in-memory transaction buffering
- [ ] Row size variants (100B vs 1KB) — adds 2x runtime but surfaces row-width scaling differences
- [ ] Event correctness verification (count events received vs events written per scenario) — strengthens recovery scenario claims
- [ ] Per-tool configuration documentation as standalone page — evaluators challenge default-config benchmarks

### Future Consideration (v2+)

- [ ] Backfill/snapshot scenario — requires per-tool methodology deep-dive to be fair; not v1.2
- [ ] EC2/cloud variant — companion to local benchmark; reproducibility cost too high for v1.2
- [ ] Multi-table / schema evolution scenario — tests ALTER TABLE mid-stream behavior
- [ ] Maxwell inclusion — only if Maxwell adds credible Postgres support

---

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Docker Compose single-command launch | HIGH | LOW | P1 |
| Steady-state throughput scenario | HIGH | MEDIUM | P1 |
| End-to-end latency (p50/p95/p99) | HIGH | MEDIUM | P1 |
| CPU% + RSS collection per tool | HIGH | LOW | P1 |
| Idle resource scenario | HIGH | LOW | P1 |
| Crash + recovery scenario | HIGH | HIGH | P1 |
| HTML report with Chart.js | HIGH | MEDIUM | P1 |
| Markdown summary | MEDIUM | LOW | P1 |
| Burst / spike scenario | HIGH | MEDIUM | P1 |
| Warmup period excluded from measurement | MEDIUM | LOW | P1 |
| Pinned Docker image versions | HIGH | LOW | P1 |
| Mixed workload (INSERT/UPDATE/DELETE) | MEDIUM | LOW | P1 |
| Methodology section in HTML report | HIGH | LOW | P1 |
| Consumer adapter per tool | HIGH | HIGH | P1 |
| Large transaction scenario | MEDIUM | MEDIUM | P2 |
| Row size variants (100B vs 1KB) | MEDIUM | LOW | P2 |
| Event correctness verification | HIGH | MEDIUM | P2 |
| Per-tool configuration documentation | MEDIUM | LOW | P2 |
| Cloud / EC2 variant | LOW | HIGH | P3 |
| Backfill scenario | MEDIUM | HIGH | P3 |
| Maxwell Postgres support | LOW | HIGH | P3 |

**Priority key:**
- P1: Must have for v1.2 launch — benchmark is not credible without these
- P2: Should have — add if timeline allows; meaningfully strengthens credibility
- P3: Nice to have — future milestone

---

## Latency Measurement Methodology

This is the most contested area in CDC benchmarking. Three approaches exist:

| Method | How It Works | Used By | Assessment |
|--------|-------------|---------|------------|
| Application timestamp in payload | Load generator writes `clock_timestamp()` to a `benchmark_ts` column; consumer reads it from CDC event payload; `latency = receipt_time - payload_ts` | Sequin's published benchmarks | Most practical; Docker containers share host clock so skew is negligible; explicitly documented; call out as methodology in report |
| WAL LSN correlation | Map LSN to wall-clock time via Postgres `pg_current_wal_lsn()` and timestamped probes; correlate event LSN to estimated WAL write time | Research papers | High complexity for marginal accuracy improvement; LSN-to-time mapping is approximate anyway; not needed for relative tool comparison |
| Consumer receipt only | Measure time between events received, not from source | Not recommended for CDC tools | Measures network and consumer performance, not CDC tool latency; cannot detect lag accumulation |

**Recommendation:** Use application timestamp (`clock_timestamp()` in `benchmark_ts` column). This is the same approach Sequin uses for their published benchmarks. For Docker-local runs, both the load generator and consumer share the host clock, so skew is negligible. Document this assumption explicitly in the HTML report methodology section.

One important note: `now()` is a common mistake. `now()` returns the transaction start time, which in a batch insert can be identical for thousands of rows inserted over several hundred milliseconds. Use `clock_timestamp()` which returns the actual wall-clock time at the moment each row is inserted.

---

## Realistic Load Levels

Validated from Sequin benchmarks and Postgres pgbench documentation:

- **10k ops/s:** Achievable on a 4-core laptop in Docker; good default for the benchmark; surfaces meaningful differences between tools without saturating Docker networking
- **50k ops/s:** Requires 8+ cores, 16 GB RAM; approaches Sequin's benchmark ceiling (60k ops/s on AWS c8g.4xlarge); demonstrates Kaptanto's throughput ceiling
- **100k+ ops/s:** Not realistic in Docker on commodity hardware; the bottleneck becomes Docker networking and the load generator, not the CDC tools; Kaptanto's theoretical throughput (500k+ events/s) cannot be demonstrated in a shared Docker environment

**Recommendation:** Default 10k ops/s with a `--rate` flag to override. Document hardware requirements for higher rates. The benchmark's value is relative comparison, not absolute numbers — load level needs to be high enough to stress-test differences, not to reproduce production throughput.

**Postgres settings required for WAL CDC (set in docker-compose.yml postgres command):**
- `wal_level = logical` — required for all WAL-based CDC tools
- `max_replication_slots = 8` — one per CDC tool (Kaptanto + Debezium + Sequin + PeerDB + spares)
- `max_wal_senders = 8` — one per replication slot consumer
- `wal_keep_size = 512MB` — prevents WAL rotation from invalidating slow consumer slots during burst scenario

---

## Competitor Benchmark Analysis

How existing CDC benchmarks are structured (evidence base for what evaluators consider credible):

| Attribute | Sequin Benchmark | Debezium Benchmark | PeerDB Claims | Kaptanto v1.2 Target |
|-----------|-----------------|-------------------|---------------|----------------------|
| Public reproducibility | GitHub repo + scripts | Terraform + EC2 (high barrier) | No public methodology | Docker Compose, zero cloud infra |
| Workload | Mixed INSERT/UPDATE/DELETE | YCSB insert-heavy | Unspecified | 60/30/10 INS/UPD/DEL |
| Load levels tested | Up to 60k ops/s, 4 row sizes | Up to 1500 ops/s | "5k TPS" unverified | 10k ops/s default, configurable |
| Latency measurement | `updated_at` application timestamp | Not reported | Undefined lag ranges | `clock_timestamp()` embedded at insert |
| Resource metrics | Not reported | CPU/RAM/network via Grafana | Not reported | CPU%/RSS per container via docker stats |
| Recovery scenario | Not tested | Not tested | Not tested | Tested — P1 |
| Idle footprint | Not tested | Not tested | Not tested | Tested — P1 |
| Report format | CSV + blog post | Grafana dashboard | Marketing page | Self-contained HTML + Markdown |

The gap in the ecosystem is clear: no existing public benchmark covers resource usage, idle footprint, and crash recovery together in a single reproducible Docker Compose harness. That gap is the differentiating value of Kaptanto v1.2's benchmark suite.

---

## Tool Inclusion Notes

| Tool | Source Support | Inclusion Decision |
|------|---------------|-------------------|
| Kaptanto | Postgres WAL (pgoutput) | Included — the tool being evaluated |
| Debezium | Postgres (pgoutput connector) | Included — the dominant incumbent; requires Kafka broker alongside |
| Sequin | Postgres (WAL) | Included — closest competitor; HTTP-based consumer |
| PeerDB | Postgres (WAL) | Included — claims strong performance; no public methodology so benchmark provides independent verification |
| Maxwell's Daemon | MySQL binlog only | Excluded — does not support Postgres; include a note in the report explaining exclusion |

Note on Debezium: Debezium requires a Kafka broker. The Docker Compose harness must include a Kafka container (single-node, not cluster) for Debezium alone. The benchmark consumer reads from Kafka topics for Debezium using a local consumer. This is the correct approach — Kafka is Debezium's required transport, not a benchmark artifact.

---

## Sources

- [Sequin Performance Docs](https://sequinstream.com/docs/performance) — 60k ops/s ceiling, mixed workload, `updated_at` timestamp methodology; MEDIUM confidence
- [Debezium Performance Blog Feb 2026](https://debezium.io/blog/2026/02/02/measuring-debezium-server-performance-mysql-streaming/) — YCSB methodology, CPU/RAM/network metrics via Grafana, EC2-based harness; HIGH confidence (official Debezium blog)
- [Debezium Quick Perf Check Jul 2025](https://debezium.io/blog/2025/07/07/quick-perf-check/) — pgbench 20 jobs/20 clients methodology, JMH for microbenchmarks, JFR flame graphs; HIGH confidence
- [PeerDB Why PeerDB Docs](https://docs.peerdb.io/why-peerdb) — 5-60s lag claims, 10-80 MBPS throughput claims, no public methodology; LOW confidence on the claims themselves
- [db-benchmarks Framework](https://db-benchmarks.com/framework/) — Docker Compose single-command pattern, cold/warm run discipline, open-source AGPLv3; MEDIUM confidence
- [Fair Benchmarking Considered Difficult — Raasveldt et al.](https://mytherin.github.io/papers/2018-dbtest.pdf) — cherry-picking workloads, configuration sensitivity factor of 28x, gaming tests; HIGH confidence (peer-reviewed DBTEST 2018)
- [Debezium vs Maxwell — Upsolver](https://www.upsolver.com/blog/debezium-vs-maxwell) — Maxwell is MySQL-only, minimalist vs Debezium JVM overhead; MEDIUM confidence
- [Debezium CDC Pain Points — Estuary](https://estuary.dev/blog/debezium-cdc-pain-points) — JVM resource requirements 4-8 GB RAM in production; MEDIUM confidence
- [TechEmpower Framework Benchmarks](https://www.techempower.com/benchmarks/) — production-grade config requirement, all implementations equal-footing, warmup discipline; HIGH confidence
- [Maxwell's Daemon Homepage](https://maxwells-daemon.io/) — confirmed MySQL binlog only, no Postgres support; HIGH confidence
- [Sequin vs Debezium Choosing Guide](https://blog.sequinstream.com/choosing-the-right-real-time-postgres-cdc-platform/) — qualitative comparison, no throughput numbers; MEDIUM confidence

---

*Feature research for: Kaptanto v1.2 CDC Benchmarking Suite*
*Researched: 2026-03-20*
