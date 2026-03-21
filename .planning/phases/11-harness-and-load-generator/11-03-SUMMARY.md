---
phase: 11-harness-and-load-generator
plan: 03
subsystem: testing
tags: [docker-compose, benchmark, cdc, harness, readme, loadgen, sequin, debezium, peerdb]

# Dependency graph
requires:
  - phase: 11-01
    provides: docker-compose.yml with all CDC services and healthchecks
  - phase: 11-02
    provides: bench/cmd/loadgen binary with four scenario modes
provides:
  - bench/README.md with prerequisites, quickstart, services table, Maxwell exclusion, loadgen reference, PeerDB setup, and teardown
  - All image tags in docker-compose.yml pinned to explicit versions (zero latest tags)
  - Human-verified harness: all services reach healthy state, loadgen idle and steady modes confirmed
affects:
  - 12-metrics-and-scenarios (uses this harness as the CDC capture environment for all scenarios)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - bench/README.md as single-source operator documentation for the harness
    - Image tag pinning audit as a pre-publish gate (zero latest tags allowed)

key-files:
  created:
    - bench/README.md
  modified:
    - bench/docker-compose.yml (tag audit confirmed — no changes required, all tags already pinned)

key-decisions:
  - "Sequin image tag v0.14.6 verified to exist — no fallback to digest-pinned latest was needed; docker-compose.yml unchanged"
  - "Maxwell's Daemon exclusion documented in README with reference to issue #434 — MySQL-only CDC tool, no Postgres support"

patterns-established:
  - "Pattern: README-first operator doc — all harness repos should have a top-level README covering prerequisites, quickstart, services table, tool exclusions, and teardown"
  - "Pattern: Zero latest tags policy — all image tags must be pinned before plan close; grep-audited at task completion"

requirements-completed: [HRN-01, HRN-02, HRN-04, LOAD-01, LOAD-02, LOAD-03]

# Metrics
duration: ~15min (includes human verification wait)
completed: 2026-03-21
---

# Phase 11 Plan 03: Harness Documentation and Smoke Test Summary

**bench/README.md with prerequisites, Maxwell exclusion (issue #434), full services table, loadgen flag reference, and PeerDB source setup — human-verified smoke test confirmed all 13 services healthy and loadgen idle + steady modes passing**

## Performance

- **Duration:** ~15 min (includes human checkpoint verification)
- **Started:** 2026-03-21T03:30:00Z
- **Completed:** 2026-03-21T03:45:00Z (approx)
- **Tasks:** 2 (Task 1 auto, Task 2 human-verify checkpoint — approved)
- **Files modified:** 1 created

## Accomplishments

- bench/README.md written with all required sections: prerequisites, quickstart (docker compose up --build), services table with all 13 services, Maxwell's Daemon exclusion with issue #434 reference, load generator build and flag reference, PeerDB source setup instructions, teardown
- Full image tag audit of bench/docker-compose.yml confirmed zero `latest` tags — all services pinned to explicit versions
- Sequin image tag `v0.14.6` verified to exist; no digest fallback required
- Human checkpoint approved: all 13 compose services reached healthy state, `./loadgen --mode idle` and `./loadgen --mode steady --rate 1000 --duration 5s` both passed

## Task Commits

Each task was committed atomically:

1. **Task 1: Version audit and bench/README.md** - `d9964e9` (feat)
2. **Task 2: Human verification checkpoint** - approved (no code commit — checkpoint, not code)

**Plan metadata:** *(this commit)*

## Files Created/Modified

- `bench/README.md` - Operator documentation: prerequisites, quickstart, services table (13 services), Maxwell exclusion with #434 reference, loadgen flag table, PeerDB source setup, teardown instructions

## Decisions Made

- Sequin tag `sequin/sequin:v0.14.6` confirmed to exist at execution time — docker-compose.yml kept unchanged, no digest fallback section added to README
- Maxwell's Daemon exclusion documented with reference to GitHub issue #434 as required by HRN-04

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Complete Phase 11 harness: all CDC tool configs, version-pinned compose, Dockerfile.bench, loadgen binary, and operator README are in place
- Human-verified: all 13 services reach healthy state and loadgen scenarios execute correctly against compose Postgres
- Phase 12 (metrics and scenarios) can mount directly against this harness — `bench/` is fully self-contained

---
*Phase: 11-harness-and-load-generator*
*Completed: 2026-03-21*

## Self-Check: PASSED

Files verified:
- bench/README.md - FOUND
- bench/docker-compose.yml - FOUND (zero latest tags confirmed)

Commits verified:
- d9964e9 (Task 1: feat(11-03): add bench/README.md)
