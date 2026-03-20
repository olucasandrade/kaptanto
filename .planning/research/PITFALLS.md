# Pitfalls Research

**Domain:** CDC benchmark comparison suite (Kaptanto vs Debezium vs PeerDB vs Maxwell's Daemon vs Sequin)
**Researched:** 2026-03-20
**Confidence:** HIGH for methodology and Docker mechanics. MEDIUM for tool-specific setup gotchas (tool configurations evolve rapidly).

---

## Critical Pitfalls

### Pitfall 1: Benchmarking Debezium Without Kafka-Overhead Separation

**What goes wrong:**
Debezium's primary deployment mode routes events through Kafka + Kafka Connect, which adds broker round-trip latency (typically 5–50ms per message depending on `linger.ms`/`batch.size`) that is entirely absent in tools like Kaptanto (stdout/SSE/gRPC with no broker). A benchmark that measures "end-to-end latency" from DB commit to consumer receipt conflates Kafka infrastructure latency with Debezium's actual CDC latency. The result: Debezium appears dramatically slower than it actually is as a CDC engine, and the benchmark gets dismissed as a vendor hit piece by the community.

The converse trap: if you strip Kafka out and test Debezium Server (its standalone mode) to make the comparison fair, you must use Debezium Server's available sinks (HTTP, Kinesis, etc.) which have their own overhead characteristics, and you must document this architectural difference explicitly.

**Why it happens:**
Benchmarks are usually written by the team that built the tool being favorably compared. The natural framing is "our architecture vs. the incumbent's architecture." This is not inherently wrong, but failing to separate "CDC engine latency" from "downstream broker latency" makes the results uninterpretable and invites legitimate criticism.

**How to avoid:**
- Run Debezium in two configurations: (a) with Kafka (showing real-world architecture overhead) and (b) Debezium Server with a local sink (showing CDC-engine-only overhead). Clearly label both.
- For latency comparisons, measure from DB commit timestamp to the moment the parsed CDC event exits the tool's output — not to when a downstream consumer reads from Kafka.
- Document the full data path for each tool in the report: `DB → CDC Engine → [Kafka →] Consumer`.
- Never describe Kafka overhead as "Debezium overhead." Kafka is infrastructure, not Debezium.

**Warning signs:**
- The benchmark only runs one Debezium deployment mode.
- Latency results for Debezium are 10–100x worse than Kaptanto without explanation.
- The report mentions "end-to-end latency" without defining the measurement endpoints for each tool.

**Phase to address:**
Phase 11 (Docker Compose harness design). The architecture of each tool's deployment must be decided before any code is written. Changing it later requires rebuilding the load generator instrumentation.

---

### Pitfall 2: JVM Cold-Start Inflation in Debezium and PeerDB Results

**What goes wrong:**
Debezium Server and PeerDB (which embeds Temporal, a JVM-adjacent workflow orchestrator) require substantial warm-up time before the JVM reaches stable throughput. JIT compilation in HotSpot transitions through interpreter → C1 tier → C2 tier, with C2 providing full optimization after hundreds of method invocations. Measurements taken in the first 30–120 seconds of a JVM-based tool's operation reflect pre-JIT performance, not steady-state throughput. If Kaptanto (Go, AOT-compiled) starts measuring immediately while Debezium is still warming up, throughput comparisons are systematically biased.

Furthermore, "mixing benchmarks within the same JVM run is wrong" (Oracle JVM benchmarking guide) — starting a new workload phase without restarting the JVM produces deoptimization artifacts from earlier phases.

**Why it happens:**
Go developers are unfamiliar with JVM warm-up requirements because Go binaries reach steady state within milliseconds. The default assumption is "wait for healthy, then measure" — but the health endpoint returns 200 long before the JVM reaches peak throughput.

**How to avoid:**
- Add a mandatory warm-up phase before any measurement: send at least 100K events through each tool and discard those measurements.
- The warm-up period should be time-bounded: minimum 60 seconds of active CDC for JVM-based tools, minimum 10 seconds for Go/Rust tools.
- Use a separate "steady-state detection" check: only begin measurement once throughput variance over a 10-second window is less than 10%.
- Document the warm-up period in the report methodology section.

**Warning signs:**
- Benchmark duration is under 2 minutes total.
- No warm-up phase in the harness.
- Debezium throughput graphs show a rising curve (still warming up) when measurements begin.

**Phase to address:**
Phase 11 (harness design). The warm-up logic belongs in the load generator, not the measurement collector.

---

### Pitfall 3: Shared Postgres Instance Causing WAL Slot Interference Between Tools

**What goes wrong:**
Running five CDC tools against the same Postgres instance creates five independent replication slots, all reading from the same WAL stream. If any one slot falls behind (slow tool, JVM GC pause, container restart), Postgres must retain WAL segments for that lagging slot, increasing disk pressure and I/O for the entire instance. This I/O contention affects other tools' throughput measurements even though they are performing correctly. A burst load scenario can cause one tool to lag, which slows the Postgres WAL subsystem, which then makes all other tools appear slower than they would be in isolation.

Additionally, each tool holds an active replication connection (a `walsender` backend process), and high-write benchmarks with five simultaneous senders amplify WAL decoding CPU on the Postgres server. The result: Postgres itself becomes the bottleneck, and measurements reflect "shared Postgres under 5x WAL decoding load" rather than each tool's individual performance.

**Why it happens:**
The natural architecture for a multi-tool benchmark is a single shared Postgres instance to ensure identical data. The interference between slots is non-obvious and does not appear in low-volume tests.

**How to avoid:**
- Configure `max_slot_wal_keep_size = 1GB` on the benchmark Postgres instance to prevent disk exhaustion from a lagging slot.
- Monitor `pg_replication_slots` throughout the benchmark: log `confirmed_flush_lsn` vs `pg_current_wal_lsn()` for each slot every 5 seconds. Alert if any slot falls more than 100MB behind.
- Run tools sequentially (one tool per benchmark run, same data) as well as simultaneously. Sequential runs measure individual tool performance without slot interference; simultaneous runs measure realistic multi-tool coexistence.
- Set `wal_level = logical` and `max_replication_slots = 10` (double the number of tools) to avoid slot exhaustion.
- In the report, disclose when measurements were taken sequentially vs. simultaneously.

**Warning signs:**
- All five tools show correlated throughput drops at the same timestamps.
- `pg_stat_replication.write_lag` growing for multiple slots simultaneously.
- Disk usage on the Postgres volume growing during the benchmark.

**Phase to address:**
Phase 11 (harness design). Slot monitoring must be built into the harness from the start. Running cleanup between tool runs (slot drop/recreate) must be automated.

---

### Pitfall 4: Docker Bridge Network Latency Artificially Inflating All Latency Numbers

**What goes wrong:**
Docker's default bridge network adds NAT/iptables overhead to every packet between containers. Measured at 0.4ms (host mode) vs. 1.2ms (bridge mode) in controlled tests — a 3x difference for container-to-container communication. For a CDC benchmark measuring sub-10ms latency, adding ~0.8ms of network overhead per event is a 8–80% artificial inflation that masks real tool differences. Worse, if different tools use different network configurations (e.g., Sequin accessing Postgres directly vs. a tool using an internal proxy), network overhead becomes an uncontrolled variable.

On macOS with Docker Desktop (which runs containers inside a Linux VM), there is an additional VM networking layer that adds latency on top of bridge networking. All benchmark numbers on macOS will be systematically higher than Linux bare-metal, which is what production deployments use.

**Why it happens:**
Docker Compose defaults to bridge networking. It works fine for functional testing where latency precision does not matter. The difference only becomes visible when measuring sub-millisecond latencies.

**How to avoid:**
- Use a dedicated Docker network with consistent configuration for all containers: `driver: bridge` with `com.docker.network.bridge.enable_ip_masquerade: "false"` where possible.
- Document the network mode and whether measurements were taken on Linux bare-metal or macOS/Docker Desktop.
- For all inter-container communication (load generator → Postgres, CDC tool → Postgres), use the container's internal DNS names (not host.docker.internal) to keep all traffic on the same bridge network.
- Clearly state in the report that latency numbers are relative comparisons within the same environment, not absolute production latency figures.
- If possible, run the authoritative benchmark on Linux bare-metal in CI (GitHub Actions Linux runner), and label macOS results as "development preview."

**Warning signs:**
- P99 latency baseline for any tool exceeds 50ms on a local machine (suggests Docker Desktop VM overhead).
- Different tools using different `network_mode` settings in the Compose file.
- Latency measurements made from outside Docker (host curl) to inside Docker containers.

**Phase to address:**
Phase 11 (harness design). Network topology must be consistent in the Compose file before measurement begins.

---

### Pitfall 5: `docker stats` Memory Reporting is Unreliable on cgroups v2

**What goes wrong:**
On Linux hosts running cgroups v2 (default on Ubuntu 22.04+, Fedora 31+), `docker stats` reports memory usage including the page cache (inactive_file), which can be orders of magnitude larger than the tool's actual RSS. The Docker CLI subtracts `inactive_file` to produce the "MEM USAGE" displayed to users, but the underlying API does not — and the definition of what counts as "cache" differs between cgroups v1 and v2. A tool that aggressively uses buffered I/O (like Badger's mmap for Kaptanto, or RocksDB for PeerDB) will show inflated memory numbers under cgroups v2, making it appear to use more memory than tools that do less I/O.

PSS (Proportional Set Size) is a more accurate metric for comparing tool memory usage, but it requires reading `/proc/[pid]/smaps_rollup` inside each container, which is not exposed by `docker stats`.

**Why it happens:**
`docker stats` is the natural first choice for memory measurement because it is built into Docker. The cgroups v1 vs. v2 behavior difference is not documented prominently and only becomes visible when comparing across host OS versions.

**How to avoid:**
- Use `docker stats --no-stream --format "{{.MemUsage}}"` for display purposes only, not as the primary metric in the report.
- For authoritative RSS measurement, exec into each container and read `/proc/1/status` (`VmRSS` field) or `smaps_rollup` (`Pss` field) via a sidecar collector script.
- Alternatively, expose a `/metrics` endpoint from each tool (Kaptanto and Debezium both have Prometheus endpoints) and collect `process_resident_memory_bytes` from the Prometheus exporter.
- Document the host OS and cgroup version used in the report. Run on a consistent kernel version to avoid measurement artifacts.
- For Kaptanto, Badger's mmap-heavy access pattern will inflate RSS on some measurements; use `go_memstats_alloc_bytes` from the Prometheus endpoint for heap allocation comparison.

**Warning signs:**
- Memory numbers varying by 5–10x between runs without corresponding workload changes.
- A tool's memory usage equal to its container's memory limit (suggests OOM kill and restart, not actual usage).
- Memory usage reported as higher than the container's `mem_limit`.

**Phase to address:**
Phase 12 (metrics collection). The measurement methodology for each metric must be defined before writing the collector, not after.

---

### Pitfall 6: Latency Clock Skew Between Containers

**What goes wrong:**
The standard CDC latency measurement is: timestamp the row change at the database (write a `created_at`/`updated_at` column to the row), and compare that timestamp to the wall clock when the CDC event is received by the consumer. This approach is only valid if the database container and consumer container agree on wall clock time. Container clocks inherit from the host kernel, so containers on the same machine should agree. However:

1. The `created_at` column timestamp records when the application wrote the row, not when the transaction committed. Network RTT between load generator and Postgres adds variable latency.
2. The WAL LSN timestamp (available via `pg_current_wal_lsn()` or the `commit_time` in `pgoutput` messages) is more accurate than the row's `updated_at` column but requires protocol version 2 to access.
3. If the load generator uses `NOW()` called in application code vs. `CURRENT_TIMESTAMP` in SQL, clock resolution differs.
4. On macOS + Docker Desktop, the VM's clock can drift from the host, adding up to tens of milliseconds of skew.

**Why it happens:**
Row timestamp-based latency measurement feels intuitive and is widely used (Sequin's own benchmarks use `updated_at` vs. Kafka consumer receipt time). The measurement is correct for relative comparisons within the same setup, but incorrectly attributed as "absolute CDC latency" when published.

**How to avoid:**
- Use a two-timestamp approach: record the row's `written_at` in the load generator (before INSERT), and compare to event receipt time in the consumer.
- Separately record and report "database commit-to-consumer" latency (using `commit_timestamp` from the WAL stream where available) vs. "application-to-consumer" latency.
- Explicitly state in the report what is being measured: "time from row INSERT to CDC event delivered at the consumer socket" — not "replication latency."
- On macOS, add a caveat that Docker Desktop's VM clock may introduce up to 10ms of systematic offset.
- Use monotonic clocks for duration calculations within a single process, wall clocks only for cross-process timestamps.

**Warning signs:**
- Latency measurements consistently negative (consumer clock is ahead of database clock — clock skew).
- Latency p50 and p99 differ by less than 1ms (suggests the measurement resolution is too coarse).
- Latency results differ by more than 20% between runs on the same hardware (suggests clock drift or GC pauses dominating).

**Phase to address:**
Phase 12 (metrics collection). Define the latency measurement protocol before writing the consumer, not after.

---

### Pitfall 7: Sequin's Hidden Infrastructure Dependencies in a "Fair" Comparison

**What goes wrong:**
Sequin requires Redis as a mandatory runtime dependency (not optional). In a Docker Compose benchmark harness, Sequin's compose definition will include a Redis container consuming CPU and memory alongside the tool itself. When reporting "Sequin memory usage = 450MB," the benchmark must clarify whether this includes the Redis container (which it should, since Redis is a required operational component). Failing to include Redis overhead makes Sequin appear lighter than it actually is in production.

Additionally, Sequin's latency is measured differently in its own benchmarks: they measure "change timestamp in row" to "event available in Kafka" (via AWS MSK), which includes Kafka producer batching latency. In a head-to-head comparison against Kaptanto's stdout mode, Sequin's Kafka-delivery latency will be higher not because of CDC engine overhead, but because Kafka batching is fundamentally different from stdout streaming.

**Why it happens:**
Each tool's marketing benchmarks measure the thing that makes that tool look best. Sequin measures Kafka delivery (its primary use case). Kaptanto's natural measurement is stdout/SSE receipt. Comparing these directly produces meaningless numbers.

**How to avoid:**
- In the Docker Compose harness, include all required infrastructure for each tool in that tool's resource usage measurements (Redis for Sequin, Temporal for PeerDB, Kafka for Debezium-with-Kafka).
- Use a consistent output sink for all tools where architecturally possible: a local HTTP endpoint receiving events is the most neutral sink (Kaptanto SSE/HTTP, Debezium Server HTTP sink, Sequin HTTP endpoint).
- Document the full resource footprint (all containers) for each tool's deployment in the report's methodology section.
- Include a "standalone resource cost" table showing the container count and baseline memory for each tool at idle.

**Warning signs:**
- Sequin memory numbers in the report do not include the Redis container.
- PeerDB memory numbers do not include the Temporal container.
- Different tools using fundamentally different output sinks in the comparison.

**Phase to address:**
Phase 11 (harness design). The "what counts as the tool's footprint" decision must be made before any measurement code is written.

---

### Pitfall 8: PeerDB's Temporal Dependency Makes "Crash Recovery" Benchmarks Apples-to-Oranges

**What goes wrong:**
PeerDB uses Temporal for workflow orchestration, which provides built-in crash recovery, retry logic, and exactly-once semantics through its state machine. When benchmarking "crash recovery time," PeerDB's recovery is fundamentally different in nature from Kaptanto's manual checkpoint-based recovery or Debezium's offset store recovery. PeerDB is not "recovering" in the same sense — Temporal re-executes the workflow from its last durable checkpoint automatically. Measuring the wall-clock time from "container killed" to "events flowing again" treats architecturally different recovery mechanisms as equivalent outcomes, which is misleading.

Furthermore, PeerDB's crash recovery requires the Temporal server itself to be running and healthy. If the benchmark kills the PeerDB container but not Temporal, the comparison is: "PeerDB with persistent orchestration vs. tool with no persistent orchestration." If the benchmark kills both, recovery includes Temporal cold-start time, which is typically 15–30 seconds.

**Why it happens:**
"Kill the container and time recovery" is a natural test. The complexity of what "recovery" means for each tool's architecture is not visible from the outside.

**How to avoid:**
- Define "recovery" precisely for each tool before writing the test: which containers are killed, what constitutes "recovered" (events flowing again? specific count of expected events received? exactly-once guarantee verified?).
- For PeerDB: kill only the peerdb-flow-worker container, not Temporal. Document this as "PeerDB worker restart" recovery. Separately test full stack restart.
- For Kaptanto: kill and restart the binary with the same Badger data directory. Measure time to resume WAL consumption.
- For Debezium: kill and restart the connector. Measure time to resume from Kafka Connect offset store.
- Present recovery time as a table with methodology footnotes, not a single bar chart.

**Warning signs:**
- A single "recovery time" metric applied to all tools without architectural context.
- PeerDB showing 0ms recovery (Temporal resumed automatically before measurement began) vs. Kaptanto showing 5s recovery.
- The report not specifying which containers were killed for each tool.

**Phase to address:**
Phase 12 (scenarios and metrics). The crash recovery scenario must be designed per-tool, not generically.

---

### Pitfall 9: Maxwell's Daemon is MySQL-Only — Including It Distorts Methodology

**What goes wrong:**
Maxwell's Daemon only supports MySQL. Including it in a Postgres CDC benchmark creates a fundamental problem: you cannot run Maxwell against the benchmark's Postgres instance. You would need either a separate MySQL instance (with different write characteristics) or exclude Maxwell from the Postgres scenarios entirely. If the benchmark includes Maxwell in some scenarios but not others without clear labeling, readers will assume all five tools were tested against the same workload, making cross-tool comparisons meaningless.

Additionally, Maxwell's architecture (single Java process, MySQL binlog, Kafka output) is closest to Debezium's MySQL connector, not to Kaptanto's pgoutput-based approach. Framing Maxwell as a direct competitor to Kaptanto in a Postgres benchmark is architecturally misleading.

**Why it happens:**
Maxwell is listed alongside Debezium in CDC tool comparison articles, which creates the impression it is a general CDC tool. The MySQL-only constraint is mentioned in documentation but missed when assembling a competitor list.

**How to avoid:**
- Scope the benchmark to Postgres CDC specifically. Maxwell is excluded from Postgres scenarios because it does not support Postgres.
- If Maxwell is included for completeness: use a MySQL 8.0 container as its source, clearly label all Maxwell results as "MySQL source," and never include Maxwell in cross-source comparisons.
- Alternatively: acknowledge Maxwell in the report as a MySQL-only tool and explain why it is excluded from the Postgres benchmark, directing readers to Maxwell's own documentation for MySQL performance characteristics.
- The simplest choice: drop Maxwell from the benchmark entirely and add a note in the report's "scope" section. Four tools against Postgres is a cleaner, more defensible comparison.

**Warning signs:**
- Maxwell included in the Postgres benchmark Docker Compose file.
- Latency or throughput charts showing Maxwell results alongside Kaptanto, Debezium, PeerDB, and Sequin without noting the MySQL source.

**Phase to address:**
Phase 11 (tool selection and harness design). Decide Maxwell's scope before writing the Compose file.

---

### Pitfall 10: OSS Community Criticism From Non-Reproducible Results

**What goes wrong:**
Publishing benchmark results without the full methodology, raw data, and reproducible harness invites legitimate criticism that permanently damages credibility. Common criticism patterns from the OSS community:

- "You didn't configure [tool X] with [tuning option Y]" — Resolved by publishing the exact configuration file for each tool.
- "Your hardware is not representative" — Resolved by publishing hardware specs and offering a cloud-reproducible alternative.
- "I ran your benchmark and got different numbers" — Resolved by publishing the exact Docker Compose file and harness code as open source.
- "You cherry-picked the scenario where your tool wins" — Resolved by including all scenarios and presenting the full results matrix, including scenarios where Kaptanto does not win.
- "The y-axis starts at non-zero to exaggerate differences" — Resolved by using zero-baseline charts for all bar charts and clearly labeled logarithmic scales for order-of-magnitude comparisons.

Sequin's own benchmark was criticized for not tuning Debezium optimally ("Debezium was deployed with configurations recommended in the AWS guide, though additional tuning was not performed by [Sequin]"). They addressed this by explicitly acknowledging it and inviting corrections — a model to follow.

**Why it happens:**
Benchmark authors focus on results, not reproducibility. Publishing raw data feels unnecessary when the charts tell the story. But without raw data, the community cannot verify claims, and any tool that scores poorly will push back.

**How to avoid:**
- Publish all benchmark code (Docker Compose, load generator, collector, report generator) as open source in the Kaptanto repository under `bench/`.
- Include a `bench/README.md` with exact reproduction steps, hardware requirements, and expected runtime.
- Publish raw CSV/JSON data alongside the HTML report. Every chart must be reproducible from the raw data.
- For each tool's configuration, commit the exact config file used (e.g., `bench/configs/debezium-connector.json`, `bench/configs/sequin.yaml`).
- Include a "what we tuned and what we left at defaults" section for each tool. Invite issues/PRs for configuration improvements.
- Present at least one scenario where a competitor outperforms Kaptanto (typically PeerDB in high-throughput Postgres-to-Postgres sync, or Debezium in Kafka ecosystem integration). Showing losses proves objectivity.
- Use zero-baseline y-axes. Never use truncated axes on bar charts for performance comparisons.

**Warning signs:**
- The harness code is not publicly available.
- Each tool's configuration is described in prose rather than committed as a file.
- Raw data is not published alongside the report.
- All scenarios show Kaptanto winning.

**Phase to address:**
Phase 13 (report generation). Reproducibility must be built into the harness from Phase 11; the report phase finalizes the publication checklist.

---

### Pitfall 11: CPU Measurement Artifacts From Docker's Per-Container Accounting

**What goes wrong:**
Docker CPU percentage reported by `docker stats` is relative to a single CPU core (100% = 1 full core). A container pinned to 4 cores can report 400%. A container that burst-processes events in batches will show 0% between bursts and 300% during bursts, making average CPU% meaningless for tools with bursty processing patterns (PeerDB's Temporal workflow batching, Debezium's Kafka producer batching). Kaptanto's streaming model processes events continuously at lower CPU%, which will appear "better" in average CPU% comparisons even if its total CPU-seconds consumed is similar.

Additionally, without CPU pinning (`cpuset` in Docker Compose), containers compete for cores, and Linux's CFS scheduler introduces latency when a container is descheduled mid-batch. This adds noise to both throughput and latency measurements.

**Why it happens:**
Average CPU% is the most intuitive metric. Bursty processing architectures are less visible because their peaks and troughs average out.

**How to avoid:**
- Report CPU usage as total CPU-seconds consumed per 1M events processed, not as instantaneous CPU%.
- Use `docker stats` polling every 100ms and compute percentile distributions (p50/p95 CPU%) rather than averages.
- Pin each tool's container to a dedicated set of CPU cores using `cpuset` in Docker Compose to prevent CPU contention between tools.
- Pin the Postgres container to separate cores from all CDC tools.
- Pin the load generator to its own cores.
- Document the core allocation in the report's methodology section.
- On a standard 8-core development machine: allocate 2 cores to Postgres, 1 core to the load generator, and 1 core per CDC tool (total 7 cores, leaves 1 for OS).

**Warning signs:**
- CPU% charts show all tools at 0% between bursts with identical average values.
- No `cpuset` configuration in the Docker Compose file.
- CPU% reported as a simple average rather than percentile distribution.

**Phase to address:**
Phase 11 (harness design) for `cpuset` configuration, Phase 12 (metrics collection) for CPU accounting methodology.

---

## Technical Debt Patterns

Shortcuts that seem reasonable but create long-term problems.

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Skip warm-up phase, start measuring immediately | Faster benchmark runtime | JVM tools show 30–60% lower throughput than steady state; results invalidated by community | Never. Warm-up is a prerequisite for credible results. |
| Use `docker stats` for all memory measurements | Zero additional tooling | cgroups v2 inflates RSS with page cache; tool comparisons misleading on modern kernels | MVP only; replace with `/proc/1/status` VmRSS before publication |
| Single run per scenario, no repetition | 5x faster iteration | Variance from GC pauses, Docker scheduling, kernel state unquantified | Development only; minimum 3 runs per scenario in published results |
| Share one Postgres instance across all tools simultaneously | Simpler harness | Slot interference causes correlated throughput drops; shared WAL I/O distorts individual results | Acceptable for functional validation; not acceptable for performance measurement |
| Cherry-pick scenarios where Kaptanto wins | Better marketing optics | Community backlash delegitimizes all other results; competing tools file issues with counter-evidence | Never. Objectivity is the entire value proposition. |
| Hard-code tool versions in Compose file without pinning | Works today | Benchmark results not reproducible when tool releases a new version; regression undetectable | Never. Always pin exact image tags (e.g., `debezium/server:3.0.0.Final`, not `latest`). |

---

## Integration Gotchas

Common mistakes when connecting CDC tools to the benchmark harness.

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| Debezium + Kafka | Testing with Kafka `linger.ms=0` (default) misses batching behavior | Use both default config AND a tuned config (`linger.ms=100`, `batch.size=1000000`). Document both. Debezium throughput nearly doubles with producer tuning (confirmed in Streamingdata.tech benchmark). |
| Sequin | Not including Redis in resource accounting | Sequin requires Redis at runtime. Report memory as `sequin_container + redis_container`. Include Redis in the Compose service group for Sequin. |
| PeerDB | Not including Temporal in resource accounting or recovery testing | PeerDB requires Temporal. Report memory as sum of all PeerDB services. For recovery tests, document which services were restarted. |
| PeerDB | Assuming parallelism=1 is comparable to Kaptanto's single-threaded WAL reader | PeerDB supports parallel snapshot reading. Set `parallelism=1` for a conservative comparison, but also run with default parallelism and document both. |
| Postgres WAL | Not setting `REPLICA IDENTITY FULL` on benchmark tables | Without FULL identity, TOAST columns in UPDATE events may be missing, causing tools to emit incomplete events. Set `REPLICA IDENTITY FULL` on all benchmark tables for a fair comparison of event completeness. |
| Load generator | Using `pgbench` directly without custom timing | pgbench suffers from "coordinated omission" (noted by Debezium's own performance engineers): it waits for each transaction to complete before sending the next, masking queueing effects. Use a custom load generator with rate-controlled INSERT/UPDATE/DELETE streams. |
| Docker Compose | Not waiting for Postgres to be fully ready (not just healthy) | The `healthy` condition from a pg_isready healthcheck passes before `wal_level=logical` is applied and publications are created. Add an explicit publication creation step with retry logic after Postgres healthcheck passes. |

---

## Performance Traps

Patterns that work in development but distort published benchmark results.

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Measuring on macOS + Docker Desktop | All latency numbers 2–5x higher than Linux bare-metal due to VM networking | Label macOS results as development-only. Run authoritative benchmarks on Linux (GitHub Actions Ubuntu runner or bare-metal). | Always — macOS Docker latency is fundamentally different from production. |
| Postgres WAL slot lag affecting peer tools | All tools show correlated throughput drops simultaneously | Monitor slot lag per tool. Stop the benchmark if any slot exceeds 100MB lag before the measurement window. | When one tool is slower than others (which is the point of the benchmark). |
| Burst workload measured with too-short window | P99 latency inflated by startup transient; throughput appears unstable | Run each scenario for minimum 60 seconds after warm-up. Use p50/p95/p99 over the full measurement window. | Burst scenarios under 30 seconds. |
| Container OOM kills silently restarting services | Tool appears to "recover fast" but actually restarted, losing in-flight events | Set memory limits high enough to prevent OOM kills during normal operation. Monitor restart counts with `docker inspect`. | Tools with high RSS (PeerDB/Temporal stack) on machines with <16GB RAM. |
| Log output from CDC tools overwhelming stdout | Host I/O becomes the bottleneck, not CDC throughput | Redirect all tool logs to files with a max size limit. Disable debug logging for all tools during benchmarks. | Any tool with verbose logging at INFO level under high throughput. |

---

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces.

- [ ] **Warm-up phase:** Often missing entirely — verify the harness sends and discards at least 100K events through each tool before recording measurements
- [ ] **JVM tool configuration:** Often at defaults — verify Debezium's Kafka producer settings are documented and a tuned configuration is tested alongside defaults
- [ ] **Resource accounting:** Often incomplete — verify Sequin measurements include Redis container, PeerDB measurements include Temporal container
- [ ] **Raw data publication:** Often charts only — verify each published chart has a linked CSV/JSON file with the underlying data
- [ ] **Zero-baseline charts:** Often truncated to exaggerate differences — verify all bar charts start at y=0
- [ ] **Tool version pinning:** Often `latest` tags — verify all Compose images use exact version tags (not `latest`, not branch-based tags)
- [ ] **Reproducibility test:** Often untested — verify a clean checkout on a fresh machine can reproduce the benchmark in under 30 minutes
- [ ] **Maxwell scope:** Often included in Postgres benchmarks — verify Maxwell is either excluded from Postgres scenarios or run against a separate MySQL container with clear labeling
- [ ] **Slot cleanup:** Often forgotten between runs — verify the harness drops and recreates replication slots between tool benchmark runs to prevent carry-over lag
- [ ] **Network consistency:** Often mixed modes — verify all containers are on the same Docker network with consistent driver settings

---

## Recovery Strategies

When pitfalls occur despite prevention, how to recover.

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Published results criticized for lacking reproducibility | HIGH | Release the full harness code as open source immediately, publish raw data, acknowledge the gap in a follow-up post, invite community re-runs |
| Debezium results dismissed as "not fairly configured" | MEDIUM | Add Debezium to the benchmark issues tracker, accept PRs for configuration improvements, re-run and update the report |
| Slot interference invalidating measurements | MEDIUM | Re-run all affected scenarios sequentially (one tool at a time), update report with note about architecture change |
| cgroups v2 inflating Kaptanto memory numbers | LOW | Switch RSS collection to `/proc/1/status` VmRSS, re-run memory scenarios, update report |
| PeerDB/Sequin infrastructure footprint not disclosed | MEDIUM | Recompute all resource metrics including dependent containers, update all memory/CPU tables in the report |
| Benchmark results showing Kaptanto losing a scenario incorrectly | LOW | Investigate whether it is a harness bug or genuine result. If harness bug: fix and re-run. If genuine: publish the result with analysis. |

---

## Pitfall-to-Phase Mapping

How roadmap phases should address these pitfalls.

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Debezium Kafka vs. standalone mode confusion | Phase 11: Harness design | Both Debezium deployment modes documented and measured in Compose file |
| JVM cold-start inflation | Phase 11: Harness design | Warm-up phase implemented; throughput stabilization check before measurement begins |
| Shared Postgres slot interference | Phase 11: Harness design | Slot lag monitoring built into harness; sequential run mode available |
| Docker bridge network latency | Phase 11: Harness design | All containers on same bridge network; `cpuset` configured; environment documented |
| `docker stats` cgroups v2 memory inaccuracy | Phase 12: Metrics collection | RSS collected from `/proc/1/status` or Prometheus `process_resident_memory_bytes` |
| Latency clock skew | Phase 12: Metrics collection | Latency measurement protocol defined; measurement endpoints documented per tool |
| Sequin Redis infrastructure omission | Phase 11: Harness design | Resource accounting includes all required containers per tool |
| PeerDB Temporal recovery measurement ambiguity | Phase 12: Scenario design | Crash recovery test specifies exactly which containers are killed per tool |
| Maxwell MySQL-only scope confusion | Phase 11: Tool selection | Maxwell explicitly excluded from Postgres scenarios with explanation in report |
| Non-reproducible results | Phase 13: Report generation | Harness code in `bench/`, raw data committed, reproduction README verified on clean machine |
| CPU measurement artifacts | Phase 11 + Phase 12 | `cpuset` in Compose; CPU reported as total CPU-seconds per 1M events |

---

## Sources

- [Measuring Debezium Server performance when streaming MySQL to Kafka (Debezium official, Feb 2026)](https://debezium.io/blog/2026/02/02/measuring-debezium-server-performance-mysql-streaming/) (HIGH confidence)
- [Benchmarking CDC Tools: Supermetal vs Debezium vs Flink CDC (Streamingdata.tech)](https://www.streamingdata.tech/p/benchmarking-cdc-tools) (HIGH confidence — includes Kafka producer tuning doubling Debezium throughput finding)
- [Improving Debezium performance (Debezium official, Jul 2025)](https://debezium.io/blog/2025/07/07/quick-perf-check/) (HIGH confidence — coordinated omission warning re: pgbench)
- [Avoiding Benchmarking Pitfalls on the JVM (Oracle)](https://www.oracle.com/technical-resources/articles/java/architect-benchmarking.html) (HIGH confidence — JVM warm-up and deoptimization mechanics)
- [Mastering Postgres Replication Slots: Preventing WAL Bloat (Gunnar Morling)](https://www.morling.dev/blog/mastering-postgres-replication-slots/) (HIGH confidence — WAL slot interference between multiple CDC tools)
- [Sequin Performance Benchmarks (Sequin official docs)](https://sequinstream.com/docs/performance) (MEDIUM confidence — Sequin's own methodology, used to identify measurement approach and Redis dependency)
- [How to Use Docker with CPU Pinning for Latency-Sensitive Apps (OneUptime, Feb 2026)](https://oneuptime.com/blog/post/2026-02-08-how-to-use-docker-with-cpu-pinning-for-latency-sensitive-apps/view) (MEDIUM confidence — cpuset latency reduction figures)
- [RSS memory data in docker stats api for cgroup2 (RealWorldAI)](https://realworldai.co.uk/post/rss-memory-data-in-docker-stats-api-for-cgroup2) (HIGH confidence — cgroups v2 memory reporting behavior)
- [docker tasks with cgroups v2 report combined RSS + cache (Hashicorp/Nomad GitHub issue #16230)](https://github.com/hashicorp/nomad/issues/16230) (HIGH confidence — confirmed cgroups v2 memory inflation behavior)
- [Benchmarking Postgres Replication: PeerDB vs Airbyte (PeerDB blog)](https://blog.peerdb.io/benchmarking-postgres-replication-peerdb-vs-airbyte) (MEDIUM confidence — parallism=1 for fair comparison methodology)
- [PeerDB self-hosting complexity discussion (Hacker News #42040917)](https://news.ycombinator.com/item?id=42040917) (MEDIUM confidence — Temporal dependency as infrastructure cost)
- [Docker Networking Modes Performance Comparison (BetterLink/EastonDev, Dec 2025)](https://eastondev.com/blog/en/posts/dev/20251217-docker-network-modes/) (MEDIUM confidence — bridge vs. host latency overhead)
- [Overcoming Pitfalls of Postgres Logical Decoding (PeerDB blog)](https://blog.peerdb.io/overcoming-pitfalls-of-postgres-logical-decoding) (HIGH confidence — REPLICA IDENTITY FULL requirement for fair comparison)
- [Maxwell's Daemon official documentation](https://maxwells-daemon.io/) (HIGH confidence — MySQL-only scope confirmed)
- [Cherry-Picking in Time Series Forecasting: How to Select Datasets (arxiv 2412.14435)](https://arxiv.org/html/2412.14435v1) (MEDIUM confidence — cherry-picking methodology framework)

---
*Pitfalls research for: CDC benchmark comparison suite (v1.2 milestone)*
*Researched: 2026-03-20*
