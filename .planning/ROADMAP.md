# Roadmap: Kaptanto

## Milestones

- ✅ **v1.0 Postgres CDC Binary** — Phases 1–7.7 (shipped 2026-03-16)
- ✅ **v1.1 Production Hardening** — Phases 8–10 (shipped 2026-03-20)
- 📋 **v1.2 Benchmark Suite** — Phases 11–13 (active)

## Phases

<details>
<summary>✅ v1.0 Postgres CDC Binary (Phases 1–7.7) — SHIPPED 2026-03-16</summary>

- [x] **Phase 1: Foundation** — Shared event types, CLI skeleton, structured logging, pure Go build setup (completed 2026-03-07)
- [x] **Phase 2: Postgres Source and Parser** — WAL consumption, pgoutput decoding, TOAST cache, schema evolution, checkpoint store (completed 2026-03-08)
- [x] **Phase 3: Event Log** — Badger-based durable append-only store with partitioning, dedup, and TTL (completed 2026-03-08)
- [x] **Phase 4: Backfill Engine** — Snapshot coordination with watermark dedup, keyset cursors, crash recovery (completed 2026-03-08)
- [x] **Phase 5: Router and stdout Output** — Partitioned routing with per-key ordering, consumer isolation, poison pill handling, NDJSON output (completed 2026-03-08)
- [x] **Phase 6: SSE and gRPC Servers** — Full output server suite with consumer cursors, filtering, metrics, and health endpoint (completed 2026-03-12)
- [x] **Phase 7: Configuration and Multi-Source** — YAML config parsing, column filtering, SQL WHERE conditions (completed 2026-03-15)
- [x] **Phase 7.1: Infrastructure Fixes** [INSERTED] — LogEntry.PartitionID fix (CHK-02), Phase 6 formal verification (completed 2026-03-15)
- [x] **Phase 7.2: Pipeline Assembly** [INSERTED] — Wire all components into runPipeline; thread config filters to consumers (completed 2026-03-15)
- [x] **Phase 7.3: Milestone Gap Closure** [INSERTED] — Fix AppendAndQueue blocking channel (INT-01) and OldTuple decode for before field (INT-02) (completed 2026-03-15)
- [x] **Phase 7.4: Backfill Pipeline Wiring** [INSERTED] — Wire BackfillEngine into runPipeline, full snapshot/backfill flows live (completed 2026-03-16)
- [x] **Phase 7.5: Observability Hardening** [INSERTED] — Wire Prometheus metrics, add healthz probes, bound SSE shutdown (completed 2026-03-16)
- [x] **Phase 7.6: Backfill Correctness** [INSERTED] — Fix watermark SnapshotLSN init (BKF-02), concurrent Run race (SRC-06), SQLite pragma (BKF-03) (completed 2026-03-16)
- [x] **Phase 7.7: Stdout Metrics** [INSERTED] — Wire EventsDelivered metric into StdoutWriter (OBS-01) (completed 2026-03-16)

Full archive: `.planning/milestones/v1.0-ROADMAP.md`

</details>

<details>
<summary>✅ v1.1 Production Hardening (Phases 8–10) — SHIPPED 2026-03-20</summary>

- [x] **Phase 8: High Availability** — Postgres advisory lock leader election with shared checkpoint store and automatic standby takeover (completed 2026-03-17)
- [x] **Phase 9: MongoDB Connector** — Change Streams consumption, BSON normalization, resume token persistence, and re-snapshot on token expiry (completed 2026-03-17)
- [x] **Phase 9.1: MongoDB HA Guard** [INSERTED] — Guard against passing MongoDB URI to Postgres HA election; INT-03 gap closure (completed 2026-03-17)
- [x] **Phase 10: Rust FFI Acceleration** — Optional Rust-accelerated pgoutput decoding, TOAST cache, and JSON serialization behind build tag (completed 2026-03-17)

Full archive: `.planning/milestones/v1.1-ROADMAP.md`

</details>

### 📋 v1.2 Benchmark Suite (Active)

**Milestone Goal:** Single-command reproducible benchmark that objectively compares Kaptanto against Debezium, PeerDB, and Sequin — generating a self-contained HTML report with charts.

- [x] **Phase 11: Harness and Load Generator** — Docker Compose with all CDC tools against shared Postgres, plus loadgen binary with scenario modes (completed 2026-03-21)
- [ ] **Phase 12: Metrics Collector and Scenarios** — Per-tool adapters writing to JSONL, all 5 benchmark scenarios executed
- [ ] **Phase 13: Report Generator** — Self-contained HTML report with charts and Markdown summary from JSONL data

## Phase Details

### Phase 11: Harness and Load Generator
**Goal**: Anyone can start the full benchmark harness with one command and generate configurable load against it
**Depends on**: Phase 10 (Kaptanto binary exists and is buildable)
**Requirements**: HRN-01, HRN-02, HRN-03, HRN-04, LOAD-01, LOAD-02, LOAD-03
**Success Criteria** (what must be TRUE):
  1. `docker compose up` in `bench/` starts Kaptanto, Debezium Server, Sequin, PeerDB, and Postgres — all services reach healthy state within 2 minutes
  2. Kaptanto service is built from source via `Dockerfile.bench` (not a pre-built image); the compose service depends on the build completing
  3. `bench/cmd/loadgen` inserts rows at configurable rates (default 10k, up to 50k ops/s), with each row containing a `_bench_ts` column from `clock_timestamp()`
  4. Load generator accepts `--mode steady|burst|large-batch|idle` and executes the correct load shape for each mode
  5. Tool versions are pinned in `docker-compose.yml`; `bench/README.md` documents Maxwell's Daemon exclusion with the issue reference
**Plans**: 3 plans

Plans:
- [ ] 11-01: Docker Compose harness — compose file with all services, healthchecks, depends_on ordering, and Dockerfile.bench
- [x] 11-02: Load generator binary — `bench/cmd/loadgen` with configurable rate, `_bench_ts` column, scenario modes (completed 2026-03-21)
- [ ] 11-03: Harness integration — verify compose+loadgen end-to-end, pin versions, write bench/README.md

### Phase 12: Metrics Collector and Scenarios
**Goal**: All five benchmark scenarios run to completion and every CDC event from every tool is captured with end-to-end timing data
**Depends on**: Phase 11
**Requirements**: MET-01, MET-02, MET-03, MET-04, SCN-01, SCN-02, SCN-03, SCN-04, SCN-05
**Success Criteria** (what must be TRUE):
  1. Running the scenario orchestrator executes all 5 scenarios in sequence (steady, burst, large-batch, crash+recovery, idle) and produces `metrics.jsonl`
  2. Each line in `metrics.jsonl` contains tool name, scenario, receive timestamp, `_bench_ts` from payload, and computed latency in microseconds
  3. Per-tool adapters receive events correctly: Kaptanto via SSE, Debezium Server via HTTP POST webhook, Sequin via HTTP push, PeerDB via Kafka
  4. `docker_stats.jsonl` contains per-container CPU% and RSS (read from `/proc/1/status` VmRSS) sampled every 2 seconds throughout all scenarios
  5. Crash+recovery scenario (SCN-04) SIGKILLs each tool and records seconds until event delivery resumes
**Plans**: 3 plans

Plans:
- [ ] 12-01: Metrics collector — `bench/cmd/collector` with per-tool adapters (SSE, webhook, HTTP push, Kafka) writing to `metrics.jsonl`
- [ ] 12-02: Docker stats poller — `/proc/1/status` VmRSS reader writing to `docker_stats.jsonl` every 2s
- [ ] 12-03: Scenario orchestrator — steady, burst, large-batch, crash+recovery, idle scenarios with collector integration

### Phase 13: Report Generator
**Goal**: A single command turns raw JSONL data into a self-contained, shareable benchmark report with charts
**Depends on**: Phase 12
**Requirements**: RPT-01, RPT-02, RPT-03, RPT-04
**Success Criteria** (what must be TRUE):
  1. `bench/cmd/reporter` reads `metrics.jsonl` and `docker_stats.jsonl` and writes a single HTML file with all JS and CSS inlined (no CDN requests, works offline)
  2. HTML report contains charts for throughput, latency (p50/p95/p99), CPU%, RSS, and recovery time — one chart per scenario per metric
  3. HTML includes a methodology section covering tool versions, hardware specs, scenario definitions, measurement approach, and Maxwell's Daemon exclusion rationale
  4. `bench/results/REPORT.md` is generated alongside the HTML file, containing Markdown tables of results and a link to the HTML report
**Plans**: 2 plans

Plans:
- [ ] 13-01: Reporter binary — `bench/cmd/reporter` reads JSONL, computes percentiles, generates data structures for charts
- [ ] 13-02: HTML + Markdown output — self-contained HTML with inlined chart library, methodology section, and REPORT.md generation

## Progress

| Phase | Milestone | Plans | Status | Completed |
|-------|-----------|-------|--------|-----------|
| 1. Foundation | v1.0 | 2/2 | ✓ Complete | 2026-03-07 |
| 2. Postgres Source and Parser | v1.0 | 3/3 | ✓ Complete | 2026-03-08 |
| 3. Event Log | v1.0 | 2/2 | ✓ Complete | 2026-03-08 |
| 4. Backfill Engine | v1.0 | 2/2 | ✓ Complete | 2026-03-08 |
| 5. Router and stdout Output | v1.0 | 3/3 | ✓ Complete | 2026-03-08 |
| 6. SSE and gRPC Servers | v1.0 | 4/4 | ✓ Complete | 2026-03-12 |
| 7. Configuration and Multi-Source | v1.0 | 4/4 | ✓ Complete | 2026-03-15 |
| 7.1–7.7. Gap Closure [INSERTED] | v1.0 | 8/8 | ✓ Complete | 2026-03-16 |
| 8. High Availability | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 9. MongoDB Connector | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 9.1. MongoDB HA Guard [INSERTED] | v1.1 | 1/1 | ✓ Complete | 2026-03-17 |
| 10. Rust FFI Acceleration | v1.1 | 3/3 | ✓ Complete | 2026-03-17 |
| 11. Harness and Load Generator | 3/3 | Complete   | 2026-03-21 | - |
| 12. Metrics Collector and Scenarios | v1.2 | 0/3 | Not started | - |
| 13. Report Generator | v1.2 | 0/2 | Not started | - |
