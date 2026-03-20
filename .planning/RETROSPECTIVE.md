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

## Milestone: v1.1 — Production Hardening

**Shipped:** 2026-03-20
**Phases:** 4 (8, 9, 9.1, 10) | **Plans:** 10 | **Timeline:** 3 days (2026-03-17 → 2026-03-20)

### What Was Built

- Postgres advisory lock leader election (`ha.LeaderElector`) — session-scoped, no TTL/clock skew, standby takes over automatically on TCP close
- Shared Postgres checkpoint store (`checkpoint.PostgresStore`) — new leader resumes from last flushed LSN without operator intervention
- MongoDB Change Streams connector (`MongoDBConnector`) — BSON normalization to unified ChangeEvent format, resume token persistence, automatic re-snapshot on token expiry
- MongoDB watermark snapshot (`MongoSnapshot`) — same keyset + watermark dedup pattern as Postgres backfill, wired into `runPipeline`
- MongoDB HA guard — explicit early-return error when `--ha` used with MongoDB source, before pgx connect attempt (INT-03)
- Rust FFI staticlib crate (`rust/kaptanto-ffi`) — decoder.rs, toast.rs, serializer.rs; cbindgen-generated C header; `make build-rust` target
- Structural equality integration tests (`parser_ffi_test.go`) — parse both build paths to JSON and compare fields; byte equality explicitly avoided due to Go map non-determinism

### What Worked

- **Lesson from v1.0 applied: wire pipeline in the same phase.** MongoDB connector was wired into `runPipeline` in Phase 9 (plan 09-03), not a gap phase. Zero wiring-related gap phases in v1.1.
- **TDD cycle continued cleanly.** Red/green commit discipline maintained across HA, MongoDB, and FFI phases. LeaderElector tests (Phase 8-02) caught lock semantics issues before wiring.
- **Decimal phase for the one actual gap (9.1).** The MongoDB + HA guard gap was caught by the audit and handled cleanly with a single decimal phase. No multi-phase gap spiral.
- **Rust FFI behind a build tag.** Keeping the Rust acceleration completely absent from the default build path meant zero disruption to the existing test suite. The structural equality test pattern is reusable for any FFI verification.

### What Was Inefficient

- **Audit was run before Phase 10 was complete**, flagging PRF-01 and PRF-03 as gaps even though Phase 10 was already planned. Audit timing matters — run after all planned phases complete.
- **ROADMAP Phase 9.1 checkbox not updated** (`[ ]` remained after completion). Same ROADMAP plan-checkbox drift as v1.0. Executor agents still don't reliably update plan-level checkboxes in ROADMAP.md.
- **SUMMARY.md one_liner field not populated** across all phases. `gsd-tools summary-extract` returned null for most v1.1 summaries. Accomplishment extraction relies on manual grep.

### Patterns Established

- **Structural equality for cross-language FFI tests**: Never use `bytes.Equal` to compare Go vs. Rust JSON output — Go map serialization is non-deterministic. Parse both to `map[string]interface{}` and compare fields recursively.
- **CGO opaque handle pattern for TOAST cache**: `newToastCache` / `setToastCache` / `getToastCache` / `freeToastCache` as C-exported Go functions. Rust holds a `*mut c_void` — no shared memory layout needed.
- **MongoDB pipeline wiring**: `runPipeline` selects connector by `cfg.SourceType()` — the same `EventLog.ReadPartition` backbone applies to both Postgres and MongoDB. No second pipeline path needed.
- **Early-return source type guards before subsystem init**: Check `cfg.SourceType()` before entering HA election block. Prevents silent crashes when incompatible flags are combined.

### Key Lessons

1. **Run the audit after all planned phases complete, not mid-milestone.** The v1.1 audit was run before Phase 10, producing false gap reports. Gate the audit on `progress_percent == 100%`.
2. **ROADMAP plan-level checkboxes need executor discipline.** Two milestones of drift. Either automate the checkbox update in the commit hook or stop relying on them — they add noise to the progress report.
3. **SUMMARY one_liner is high-value metadata.** Accomplishment extraction, MILESTONES.md, and retrospective writing all depend on it. Make one_liner a required frontmatter field with a linter check.
4. **The decimal-phase pattern scales well for genuine gaps.** Phase 9.1 was a single-plan fix that closed INT-03 without disrupting the main phase sequence. Keep using decimal phases for post-audit gap closure; resist the temptation to include gap work in ongoing phases.

### Cost Observations

- Model: sonnet (executor + verifier throughout; yolo mode)
- Sessions: ~4 (one per phase group)
- Notable: 3-day wall-clock for a meaningful feature set (HA + MongoDB + Rust FFI) with TDD. The v1.0 lesson about early integration wiring paid off — no multi-phase gap spiral in v1.1.

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Phases | Plans | Unplanned % | Key Change |
|-----------|--------|-------|-------------|------------|
| v1.0 | 14 | 32 | 50% (7 gap phases) | First implementation — established patterns |
| v1.1 | 4 | 10 | 25% (1 gap phase: 9.1) | Applied v1.0 lessons — wired pipeline in-phase; one audit gap |

### Cumulative Quality

| Milestone | LOC | Test Packages | Zero-CGO default | Phases Verified |
|-----------|-----|---------------|------------------|-----------------|
| v1.0 | 10,749 Go | 15+ | ✓ | 14/14 |
| v1.1 | 13,873 Go + 336 Rust | 18+ | ✓ | 18/18 |

### Top Lessons (Validated Across Milestones)

1. **Wire integration paths within the same phase that builds the components.** v1.0: 7 gap phases for wiring. v1.1: 1 gap phase (a genuine guard, not wiring). Lesson confirmed.
2. **Metrics scrapeability (HTTP endpoint) is distinct from metrics instrumentation.** Still unresolved in v1.1 (stdout mode has no HTTP server). Carries to v1.2.
3. **Watermark and ordering invariants need table-driven tests at design time.** Maintained in v1.1 MongoDB snapshot — no watermark bugs. Lesson confirmed.
4. **Run the audit only after all planned phases complete.** Premature audit in v1.1 produced false gap reports for Phase 10. New rule: audit gate requires 100% phase completion.
