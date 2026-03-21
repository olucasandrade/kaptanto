---
gsd_state_version: 1.0
milestone: v1.2
milestone_name: Benchmark Suite
status: unknown
last_updated: "2026-03-21T09:11:31.830Z"
progress:
  total_phases: 21
  completed_phases: 21
  total_plans: 50
  completed_plans: 50
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-20)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v1.2 — Benchmark Suite

## Current Position

Phase: 13 of 21 (Reporter) — COMPLETE
Plan: 02 complete (13-reporter-02-SUMMARY.md written)
Status: complete
Last activity: 2026-03-21 — 13-02 reporter HTML renderer, Markdown writer, cmd/reporter binary

Progress: [████████████████████] 50/50 plans (100%)

## Performance Metrics

**Velocity:**
- Total plans completed: 43
- Average duration: ~4 min
- Total execution time: ~2.8 hours

**By Phase (recent):**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 08-high-availability | 3 | ~9 min | 3 min |
| 09-mongodb-connector | 3 | ~535 min | ~178 min |
| 10-rust-ffi-acceleration | 3 | ~374 min | ~125 min |
| 11-harness-and-load-generator | 3/3 | ~25 min | ~8 min |

**Recent Trend:**
- Last plan: 12-03 scenario orchestrator and runner (~3 min)
- Trend: Phase 12 in progress, plans 01, 02 and 03 complete

*Updated after each plan completion*
| Phase 12-metrics-collector-and-scenarios P03 | 179s | 2 tasks | 3 files |
| Phase 12-metrics-collector-and-scenarios P02 | 193s | 2 tasks | 6 files |
| Phase 12-metrics-collector-and-scenarios P01 | 5 | 3 tasks | 10 files |
| Phase 13-reporter P01 | 166 | 2 tasks | 4 files |
| Phase 13-reporter P02 | 289 | 3 tasks | 9 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [v1.2 Roadmap]: 3-phase structure derived from natural delivery boundaries — harness before metrics before report; each phase is a complete, independently verifiable capability
- [v1.2 Roadmap]: Phase 12 combines MET and SCN requirements — collector and scenarios are tightly coupled (scenarios drive the collector); separating them would leave either phase non-verifiable
- [v1.2 Roadmap]: Maxwell's Daemon excluded from harness — MySQL-only (no Postgres CDC), confirmed by maintainer issue #434; documented in bench/README.md (HRN-04)
- [v1.2 Roadmap]: RSS sourced from /proc/1/status VmRSS not `docker stats` — docker stats RSS includes shared memory; VmRSS is process-private (MET-04)
- [11-02 Load Generator]: bench/ is a separate Go module to isolate benchmark dependencies from production binary
- [11-02 Load Generator]: burst=50000 hardcoded in rate.NewLimiter — prevents WaitN error at any --rate value up to 50k
- [11-02 Load Generator]: CopyFrom with client-side time.Now() used for all modes — semantically equivalent for end-to-end CDC latency measurement
- [11-02 Load Generator]: stdlib flag package used (not cobra) — loadgen is a simple single-purpose tool
- [11-01 Harness]: Debezium sink is redis — reuses shared redis already needed by Sequin, avoids extra drain service
- [11-01 Harness]: flow-snapshot-worker included in PeerDB set — avoids missing-worker errors on startup
- [11-01 Harness]: Isolated internal Postgres per CDC tool (sequin-postgres, peerdb-postgres) — source postgres is CDC source only
- [Phase 11]: Sequin image tag v0.14.6 verified at execution time — no digest fallback needed, docker-compose.yml unchanged
- [Phase 12-metrics-collector-and-scenarios]: docker:cli runtime for Dockerfile.statsd — statsd calls exec.Command(docker) for inspect and stats; distroless lacks the docker CLI binary
- [Phase 12-metrics-collector-and-scenarios]: vmrss_kb JSON field name (not rss_kb) — Phase 13 report generator reads this field by name from docker_stats.jsonl
- [Phase 12-metrics-collector-and-scenarios]: Debezium sink switched redis->http pointing collector:8081/ingest/debezium — without this MET-02 is unsatisfiable (zero events reach collector)
- [Phase 12-metrics-collector-and-scenarios]: RunKaptanto/RunPeerDB naming: disambiguated Run functions in same adapters package to avoid compile error without splitting into sub-packages
- [Phase 12-metrics-collector-and-scenarios]: Fan-out goroutine pattern: adapterCh -> fan-out updates lastSeen -> records -> writer keeps management API reads consistent without blocking adapters
- [Phase 12-metrics-collector-and-scenarios]: Always-200 before processing in Debezium/Sequin handlers: prevents retry floods from CDC sinks treating non-2xx as retriable
- [12-03 Scenario Orchestrator]: ScenarioDef.PreWaitS=30 for steady (warmup configurable without changing loadgen flags)
- [12-03 Scenario Orchestrator]: buildLoadgenCmd always prepends --dsn to loadgen args — required by loadgen CLI
- [12-03 Scenario Orchestrator]: pollRecovery returns elapsed regardless of timeout — no -1 sentinel; caller logs the value
- [Phase 13-reporter]: StatRecord defined in reporter package (not imported from statsd) — avoids cross-package import while maintaining identical JSON field names
- [Phase 13-reporter]: Latencies sorted inside Aggregate, not ParseMetrics — parse phase is accumulation-only; sorting is an aggregation concern
- [Phase 13-reporter]: No external dependencies added for reporter data pipeline — slices.Sort and math.Ceil are stdlib (Go 1.21+)
- [Phase 13-reporter]: go:embed paths cannot contain '..' so Chart.js asset placed at bench/internal/reporter/assets/ (package-adjacent) instead of cmd/reporter/assets/
- [Phase 13-reporter]: template.JS wraps all trusted JS content in html/template script blocks — chart library and chart data JSON; prevents HTML-escaping of < > & characters
- [Phase 13-reporter]: bench/results/report.html excluded from git (.gitignore) — changes on every run due to GeneratedAt; only REPORT.md committed

### Pending Todos

None yet.

### Blockers/Concerns

None yet.

## Session Continuity

Last session: 2026-03-21
Stopped at: Phase 13 Plan 02 complete — HTML renderer with 7 Chart.js charts, Markdown writer, cmd/reporter binary (13-reporter-02-SUMMARY.md written)
Resume with: Milestone v1.2 complete — all 50 plans executed
