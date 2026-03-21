---
gsd_state_version: 1.0
milestone: v1.2
milestone_name: Benchmark Suite
status: in_progress
last_updated: "2026-03-21"
progress:
  total_phases: 21
  completed_phases: 18
  total_plans: 50
  completed_plans: 44
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-20)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v1.2 — Benchmark Suite

## Current Position

Phase: 11 of 21 (Harness and Load Generator)
Plan: 01 complete (11-01-SUMMARY.md written)
Status: in_progress
Last activity: 2026-03-21 — 11-01 Docker Compose harness, Dockerfile.bench, and CDC tool configs

Progress: [████████░░░░░░░░░░░░] 44/50 plans (88%)

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
| 11-harness-and-load-generator | 2/3 | ~10 min | 5 min |

**Recent Trend:**
- Last plan: 11-01 Docker Compose harness (2 min)
- Trend: establishing v1.2 baseline

*Updated after each plan completion*

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

### Pending Todos

None yet.

### Blockers/Concerns

None yet.

## Session Continuity

Last session: 2026-03-21
Stopped at: Phase 11 Plan 01 complete — Docker Compose harness built (bench/docker-compose.yml)
Resume with: /gsd:execute-phase 11 (plan 03 — next plan in phase)
