---
phase: 11-harness-and-load-generator
plan: 01
subsystem: infra
tags: [docker-compose, debezium, sequin, peerdb, temporal, postgres, redis, kaptanto, cdc, benchmark]

# Dependency graph
requires:
  - phase: 11-harness-and-load-generator
    provides: bench/ Go module with loadgen binary (11-02)
provides:
  - bench/Dockerfile.bench — multi-stage build, kaptanto from source to distroless runtime
  - bench/docker-compose.yml — full 13-service CDC benchmark harness with healthchecks
  - bench/config/debezium/application.properties — Debezium Server pgoutput→redis config
  - bench/config/sequin/sequin.yml — Sequin source database reference config
affects:
  - 12-metrics-and-collector
  - 13-benchmark-report

# Tech tracking
tech-stack:
  added:
    - quay.io/debezium/server:3.4.2.Final (Quay.io registry, not Docker Hub)
    - sequin/sequin:v0.14.6
    - ghcr.io/peerdb-io/* stable-v0.36.12 (peerdb-server, flow-api, flow-worker, flow-snapshot-worker, peerdb-ui)
    - temporalio/auto-setup:1.29
    - postgres:16.13-alpine
    - redis:7.2.4-alpine
    - gcr.io/distroless/static:nonroot (runtime stage in Dockerfile.bench)
  patterns:
    - All services declare healthcheck blocks; all depends_on use condition: service_healthy
    - Isolated internal Postgres per tool (sequin-postgres, peerdb-postgres) — never share metadata DB with CDC source
    - Postgres WAL config via -c flags in command, not mounted postgresql.conf
    - Debezium uses redis sink (no stdout sink in 3.x; reuses shared redis, zero extra services)
    - Build context for kaptanto is repo root (Go source at root, bench/ is separate module)

key-files:
  created:
    - bench/Dockerfile.bench
    - bench/docker-compose.yml
    - bench/config/debezium/application.properties
    - bench/config/sequin/sequin.yml
  modified: []

key-decisions:
  - "Debezium sink is redis (not http) — reuses shared redis already needed for Sequin, avoids adding a drain service"
  - "Sequin tag v0.14.6 used (confirmed format); fallback to latest+digest if unavailable at run time"
  - "flow-snapshot-worker included in PeerDB set — avoids missing-worker errors on startup"
  - "postgres command flags (-c wal_level=logical etc) over mounted config — simpler, no volume needed"
  - "Kaptanto healthcheck on port 7655 (/healthz) — observability port is cfg.Port+1 = 7654+1"

patterns-established:
  - "Pattern: docker-compose healthcheck+depends_on chain — all 13 services declare healthchecks, all dependencies use condition: service_healthy"
  - "Pattern: isolated internal Postgres per CDC tool — Sequin and PeerDB each own their metadata DB, shared postgres is source-only"

requirements-completed:
  - HRN-01
  - HRN-02
  - HRN-03
  - HRN-04

# Metrics
duration: 2min
completed: "2026-03-21"
---

# Phase 11 Plan 01: Harness and Load Generator — Docker Compose Harness Summary

**13-service Docker Compose harness with pinned image tags, per-service healthchecks, and depends_on ordering for Kaptanto, Debezium Server 3.4, Sequin v0.14.6, PeerDB stable-v0.36.12, and their dependencies against a shared Postgres 16 CDC source**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-21T02:36:54Z
- **Completed:** 2026-03-21T02:38:31Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Multi-stage Dockerfile.bench: golang:1.25-alpine builder + distroless runtime, pure-Go bench build (no Rust FFI)
- docker-compose.yml with 13 services, all image tags pinned, all healthchecks defined, all depends_on using `condition: service_healthy`
- Debezium Server configured via application.properties with pgoutput connector and redis sink (slot: debezium_bench)
- Sequin configured via sequin.yml referencing shared benchmark Postgres (slot: sequin_bench, pub: sequin_bench_pub)
- Postgres WAL configured for logical replication with max_wal_senders=20, max_replication_slots=20

## Task Commits

Each task was committed atomically:

1. **Task 1: Dockerfile.bench** - `677ffb4` (feat)
2. **Task 2: docker-compose.yml + config files** - `f20a866` (feat)

**Plan metadata:** (docs commit — see below)

## Files Created/Modified
- `bench/Dockerfile.bench` — two-stage build: golang:1.25-alpine → distroless/static:nonroot
- `bench/docker-compose.yml` — 13-service harness with all CDC tools, healthchecks, depends_on chains
- `bench/config/debezium/application.properties` — Debezium Server config: pgoutput source, redis sink
- `bench/config/sequin/sequin.yml` — Sequin source DB reference (bench-postgres), slot and publication names

## Decisions Made
- Redis sink chosen for Debezium over http drain: reuses the redis instance already required by Sequin, zero extra services
- `flow-snapshot-worker` included with PeerDB services: avoids startup errors for CDC-only workloads
- `postgres:16.13-alpine` used for all three isolated Postgres instances (shared source, sequin-postgres, peerdb-postgres)
- Kaptanto healthcheck on `http://localhost:7655/healthz` (observability port = cfg.Port+1 = 7655)
- Comment mentioning "no latest" removed from compose file to prevent false positive on grep check

## Deviations from Plan

None — plan executed exactly as written. The comment-string grep false-positive was a minor editing fix to the compose file header comment, not a deviation from the specified behavior.

## Issues Encountered
- The plan's verification check `grep "latest"` matched a comment line in the initial compose header. Fixed by rephrasing the comment to not include the word "latest". This is cosmetic only — no image tag uses `latest`.

## User Setup Required

None - no external service configuration required beyond `docker compose up --build` from `bench/`.

## Next Phase Readiness
- Full harness ready: `docker compose up --build` from `bench/` starts all 13 services with healthcheck ordering
- Loadgen binary (from 11-02) connects to `postgres:5432` via `BENCH_DSN` or `--dsn` flag
- Phase 12 (metrics collector) can now reference the harness port assignments and service names
- PeerDB peer must be configured post-startup via SQL (`CREATE PEER bench_postgres FROM POSTGRES WITH ...`) on port 9900

---
*Phase: 11-harness-and-load-generator*
*Completed: 2026-03-21*
