# Requirements: Kaptanto

**Defined:** 2026-03-20
**Milestone:** v1.2 Benchmark Suite
**Core Value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## v1.2 Requirements

Requirements for the v1.2 milestone. Each maps to roadmap phases.

### Harness

- [x] **HRN-01**: `docker compose up` starts all tools (Kaptanto, Debezium Server, Sequin, PeerDB + dependencies) against a shared Postgres instance
- [x] **HRN-02**: Each service has a healthcheck and `depends_on` so the harness waits for full readiness before scenarios begin
- [x] **HRN-03**: Kaptanto is built from source and containerized via `Dockerfile.bench` as a compose service
- [x] **HRN-04**: Tool versions are pinned in the compose file; Maxwell's Daemon exclusion is documented in `bench/README.md`

### Load Generator

- [x] **LOAD-01**: `bench/cmd/loadgen` inserts rows into Postgres at a configurable rate (default 10k ops/s, configurable up to 50k)
- [x] **LOAD-02**: Each row contains a `_bench_ts` column populated by `clock_timestamp()` for end-to-end latency measurement
- [x] **LOAD-03**: Load generator supports scenario modes: steady, burst (0→50k ops/s spike), large-batch (100k row single tx), idle

### Metrics Collection

- [x] **MET-01**: `bench/cmd/collector` receives CDC events from all tools via per-tool adapters and writes per-event records to `metrics.jsonl`
- [x] **MET-02**: Adapters implemented for: Kaptanto (SSE), Debezium Server (HTTP POST webhook), Sequin (HTTP push), PeerDB (Kafka)
- [x] **MET-03**: Each event record contains: tool, scenario, receive timestamp, `_bench_ts` from payload, computed latency (µs)
- [x] **MET-04**: Container CPU% and RSS memory polled every 2s into `docker_stats.jsonl`; RSS sourced from `/proc/1/status` VmRSS (not `docker stats`)

### Scenarios

- [ ] **SCN-01**: Steady-state: 10k ops/s for 60s after 30s warmup — measures peak throughput and p50/p95/p99 latency
- [ ] **SCN-02**: Burst: 0→50k ops/s spike for 10s then back to 10k — measures overload recovery
- [ ] **SCN-03**: Large-batch: single transaction of 100k rows — measures latency tail under bulk insert
- [ ] **SCN-04**: Crash+recovery: SIGKILL each CDC tool, measure seconds until delivery resumes
- [ ] **SCN-05**: Idle resource: 60s at zero load — measures baseline CPU% and RSS at rest

### Report

- [ ] **RPT-01**: `bench/cmd/reporter` reads `metrics.jsonl` + `docker_stats.jsonl` and generates a self-contained HTML file (JS/CSS inlined, no CDN)
- [ ] **RPT-02**: HTML report includes charts for: throughput, latency (p50/p95/p99), CPU%, RSS, recovery time — one chart per scenario per metric
- [ ] **RPT-03**: HTML includes methodology section: tool versions, hardware, scenarios, measurement approach, Maxwell exclusion rationale
- [ ] **RPT-04**: Reporter generates `bench/results/REPORT.md` (Markdown tables + link to HTML) alongside raw `metrics.jsonl` and `docker_stats.jsonl`

## Future Requirements

### Operations

- **OPS-01**: Management REST API (GET/POST sources, tables, consumers, backfills)
- **OPS-02**: Badger value log GC on periodic ticker for disk reclamation

### Configuration

- **CFG-07**: SIGHUP hot-reload for adding/removing tables without restart
- **CFG-08**: Dynamic table addition via ALTER PUBLICATION

### Distribution

- **DST-01**: Docker multi-stage build (Rust → Go → scratch)
- **DST-02**: Homebrew tap
- **DST-03**: curl installer script
- **DST-04**: GitHub Actions CI (test, lint, build, release)

## Out of Scope

| Feature | Reason |
|---------|--------|
| Maxwell's Daemon | MySQL-only; no Postgres CDC support (issue #434 confirmed by maintainer) |
| MySQL connector benchmark | No MySQL source in Kaptanto v1.2 |
| Cloud-hosted benchmark results | Results are reproducible locally; hosting adds infra dependency |
| Automated CI benchmark runs | GitHub Actions benchmark runner deferred (DST-04 prerequisite) |
| Kafka as benchmark output for Kaptanto | Kaptanto uses SSE/gRPC/stdout; Kafka sink not in scope for v1.x |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| HRN-01 | Phase 11 | Complete |
| HRN-02 | Phase 11 | Complete |
| HRN-03 | Phase 11 | Complete |
| HRN-04 | Phase 11 | Complete |
| LOAD-01 | Phase 11 | Complete (11-02) |
| LOAD-02 | Phase 11 | Complete (11-02) |
| LOAD-03 | Phase 11 | Complete (11-02) |
| MET-01 | Phase 12 | Complete |
| MET-02 | Phase 12 | Complete |
| MET-03 | Phase 12 | Complete |
| MET-04 | Phase 12 | Complete |
| SCN-01 | Phase 12 | Pending |
| SCN-02 | Phase 12 | Pending |
| SCN-03 | Phase 12 | Pending |
| SCN-04 | Phase 12 | Pending |
| SCN-05 | Phase 12 | Pending |
| RPT-01 | Phase 13 | Pending |
| RPT-02 | Phase 13 | Pending |
| RPT-03 | Phase 13 | Pending |
| RPT-04 | Phase 13 | Pending |

**Coverage:**
- v1.2 requirements: 20 total
- Mapped to phases: 20
- Unmapped: 0 ✓

---
*Requirements defined: 2026-03-20*
*Last updated: 2026-03-20 after initial definition*
