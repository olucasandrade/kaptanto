# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

---

## Milestone: v1.0 — Postgres CDC Binary

**Shipped:** 2026-03-16
**Phases:** 14 (7 planned + 7 gap-closure) | **Plans:** 32 | **Timeline:** 9 days (2026-03-07 → 2026-03-16)

### What Was Built

- Full Postgres WAL pipeline: pgoutput decoder, TOAST cache, REPLICA IDENTITY validation, multi-host failover, exponential backoff reconnect
- Durable BadgerDB Event Log with FNV-1a partition routing, deduplication by event ID, and configurable TTL
- Backfill engine with keyset cursors (no OFFSET), watermark deduplication against live WAL, SQLite crash-resumable state, and 5 snapshot strategies
- Partitioned router with per-key ordering guarantees, poison-pill isolation, and exponential backoff retry scheduler
- Three output modes: stdout NDJSON, SSE with browser-native streaming + cursor persistence, gRPC bidirectional Subscribe/Acknowledge
- Full `runPipeline` component graph: config YAML → column/row filters → consumers; errgroup lifecycle with bounded shutdown
- Prometheus metrics on all components; `/healthz` with 4 real component probes

### What Worked

- **TDD cycle discipline**: Every plan started with failing tests (RED commits), then implementation (GREEN commits). Catches interface mismatches before wiring.
- **Phase verifier after each execution**: Automated VERIFICATION.md after every phase caught documentation gaps (Phase 02), wiring issues, and requirement drift early.
- **Decimal phase insertions**: Gap-closure phases (7.1–7.7) kept the main phase sequence clean and made it obvious what was unplanned work vs. planned.
- **Bottom-up phase order**: Building the Event Log before the router, and the router before outputs, meant each phase had a stable foundation. No rework of lower layers.
- **SetMetrics setter pattern**: Avoids circular constructor dependencies between output writers and the shared metrics registry. Should be the standard going forward.

### What Was Inefficient

- **7 gap-closure phases added**: 50% of phases were unplanned insertions. Root causes: (1) integration gaps not caught until Phase 7 wired everything together, (2) backfill bugs only surfaced in the full pipeline. Earlier integration testing would have caught INT-01, BKF-02, BKF-03 before dedicated gap phases were needed.
- **REQUIREMENTS.md checkbox drift**: Phase 02 verification flagged unchecked boxes as a `gaps_found` status, requiring a Phase 7.1 documentation fix. Verifier agents should update checkboxes inline during plan execution.
- **No HTTP server in stdout mode**: Metrics and healthz unreachable in default mode. Should have been part of Phase 7.5 scope, not deferred to tech debt.
- **Phase 7 plans show `[ ]` in ROADMAP.md**: ROADMAP plan checkboxes for 02-02 and 02-03 were never updated. Executor agents don't reliably update ROADMAP plan-level checkboxes.

### Patterns Established

- **EventLog as pipeline backbone**: Router reads from `EventLog.ReadPartition`, not from the connector channel. This enables crash recovery and decouples backfill events from WAL events naturally.
- **Non-blocking AppendAndQueue with drain-or-drop**: Prevents WAL receive goroutine from stalling under slow consumers while preserving the durable write to EventLog. Use this pattern for any unbuffered producer→consumer boundary.
- **db.Exec pragma pattern**: Apply SQLite pragmas via `db.Exec("PRAGMA journal_mode=WAL;")` after open — never via URI parameters. URI pragma format is unreliable with modernc.org/sqlite.
- **`//go:build integration` tag**: Integration tests requiring live Postgres stay out of the default `go test ./...` run. Keep this separation.

### Key Lessons

1. **Wire the pipeline early, not late.** The most expensive gap-closure phases (7.2–7.4) were needed because components were built in isolation and only wired in Phase 7. For v1.1, wire MongoDB into `runPipeline` within the same phase that builds the connector.
2. **Test cross-component invariants in every phase.** CHK-01 (EventLog.Append before checkpoint advance) was verified per-phase but never end-to-end until Phase 7.3. Add a pipeline integration test that verifies the full ordering invariant.
3. **Watermark semantics need a concrete test from day one.** BKF-02 (SnapshotLSN=0 inverts dedup) was a silent logic inversion that only manifested when the full backfill+WAL pipeline was live. Add a table-driven test for `ShouldEmit` at backfill design time.
4. **Metrics endpoints need to be reachable in every mode.** OBS-01 gap: counter increments are not the same as a scrapeable endpoint. Address `/metrics` HTTP availability in the same phase that wires metrics, regardless of output mode.

### Cost Observations

- Model: sonnet (executor + verifier agents throughout)
- Sessions: ~14 (one per major phase group)
- Notable: Parallel wave execution within phases (where phases had multiple plans) measurably reduced wall-clock time. The 7 gap-closure phases each completed in under 10 minutes with single-plan wave execution.

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Phases | Plans | Unplanned % | Key Change |
|-----------|--------|-------|-------------|------------|
| v1.0 | 14 | 32 | 50% (7 gap phases) | First implementation — established patterns |

### Cumulative Quality

| Milestone | Go LOC | Test Packages | Zero-CGO | Phases Verified |
|-----------|--------|---------------|----------|-----------------|
| v1.0 | 10,749 | 15+ | ✓ | 14/14 |

### Top Lessons (To Validate Across Milestones)

1. Wire integration paths within the same phase that builds the components — not in a dedicated later phase.
2. Metrics scrapeability (HTTP endpoint) is distinct from metrics instrumentation (counter increment). Verify both together.
3. Watermark and ordering invariants need table-driven tests at design time, not after integration.
