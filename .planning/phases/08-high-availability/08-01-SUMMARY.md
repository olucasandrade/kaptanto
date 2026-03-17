---
phase: 08-high-availability
plan: 01
subsystem: database
tags: [postgres, checkpoint, ha, pgx, cdc]

# Dependency graph
requires:
  - phase: 02-postgres-source-and-parser
    provides: pgx/v5 already in go.mod; CheckpointStore interface in internal/checkpoint/store.go
  - phase: 01-foundation
    provides: CheckpointStore interface, SQLiteStore pattern to mirror
provides:
  - PostgresStore implementing CheckpointStore backed by shared Postgres table
  - postgres_checkpoints table auto-created on first Open()
  - Ping(ctx) health probe method for HA health handler
affects: [08-02, 08-03, 09-mongodb, 10-rust-ffi]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - pgx.Conn (single connection, not pool) for per-process HA store
    - ON CONFLICT (source_id) DO UPDATE upsert pattern with updated_at=NOW()
    - var _ CheckpointStore = (*PostgresStore)(nil) compile-time interface assertion
    - t.Skip("set POSTGRES_TEST_DSN...") integration test skip pattern

key-files:
  created:
    - internal/checkpoint/postgres.go
    - internal/checkpoint/postgres_test.go
  modified: []

key-decisions:
  - "PostgresStore uses pgx.Conn (single connection) not pgxpool — HA runs one instance per process; pool idle connections add complexity with no benefit"
  - "OpenPostgres takes DSN string not *pgx.Conn — matches Open() on SQLiteStore; callers in runPipeline use cfg.Source directly"
  - "Ping(ctx) accepts context param (unlike SQLiteStore.Ping()) — enables 2s timeout from caller, mirrors Postgres health probe in root.go"
  - "Integration tests skip with t.Skip when POSTGRES_TEST_DSN unset — graceful CI behavior without Postgres container"

patterns-established:
  - "Postgres integration test skip: os.Getenv('POSTGRES_TEST_DSN') == '' → t.Skip"
  - "pgx ErrNoRows guard: errors.Is(err, pgx.ErrNoRows) replaces database/sql.ErrNoRows"

requirements-completed: [CHK-05]

# Metrics
duration: 2min
completed: 2026-03-17
---

# Phase 8 Plan 1: PostgresStore Shared Checkpoint Summary

**Postgres-backed CheckpointStore using pgx.Conn with auto-created postgres_checkpoints table, enabling both HA instances to share the leader's last committed LSN**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-17T00:24:28Z
- **Completed:** 2026-03-17T00:26:17Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 2

## Accomplishments

- PostgresStore implementing CheckpointStore with pgx native API (not database/sql)
- postgres_checkpoints table auto-created in OpenPostgres() — no manual migration needed
- Compile-time interface assertion `var _ CheckpointStore = (*PostgresStore)(nil)`
- Integration tests with graceful skip when POSTGRES_TEST_DSN is unset

## Task Commits

Each task was committed atomically:

1. **RED: Failing tests for PostgresStore** - `6889777` (test)
2. **GREEN: PostgresStore implementation** - `435635c` (feat)

_Note: TDD plan — RED commit then GREEN commit per TDD protocol_

## Files Created/Modified

- `internal/checkpoint/postgres.go` - PostgresStore with OpenPostgres, Save, Load, Close, Ping
- `internal/checkpoint/postgres_test.go` - Integration tests (skip without POSTGRES_TEST_DSN)

## Decisions Made

- Used `pgx.Conn` (single connection) not `pgxpool.Pool` — HA runs one process per instance; pool complexity without benefit
- `OpenPostgres(ctx, dsn string)` signature mirrors `Open(path string)` on SQLiteStore — consistent constructor pattern
- `Ping(ctx context.Context)` accepts context (unlike SQLiteStore.Ping()) so callers control the 2s timeout
- Test package `checkpoint_test` (black-box) imports `checkpoint` explicitly — same pattern as sqlite_test.go

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Added missing package import in external test file**
- **Found during:** GREEN phase (running tests)
- **Issue:** Test file used `package checkpoint_test` (black-box) but did not import `github.com/kaptanto/kaptanto/internal/checkpoint`, causing `OpenPostgres` to be undefined
- **Fix:** Added the package import and prefixed all `OpenPostgres` calls with `checkpoint.`
- **Files modified:** internal/checkpoint/postgres_test.go
- **Verification:** `go test ./internal/checkpoint/... -run TestPostgresStore -v` passes (skips gracefully)
- **Committed in:** `435635c` (GREEN commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 - missing import in external test package)
**Impact on plan:** Necessary fix for compilation; no scope change.

## Issues Encountered

- External test package (`_test` suffix) requires explicit package import — corrected inline during GREEN phase without additional commits (fix bundled into GREEN commit).

## User Setup Required

To run the integration tests against a live Postgres instance:

```bash
export POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/testdb"
go test ./internal/checkpoint/... -run TestPostgresStore -v
```

Tests skip gracefully without this variable — no CI configuration change required.

## Next Phase Readiness

- PostgresStore (CHK-05) is ready for wiring in Phase 8 Plan 2 (HA advisory lock / leader election)
- Both instances can share the same DSN and read each other's checkpoint writes
- Health handler can call `store.Ping(ctx)` for liveness checks

---
*Phase: 08-high-availability*
*Completed: 2026-03-17*

## Self-Check: PASSED

- FOUND: internal/checkpoint/postgres.go
- FOUND: internal/checkpoint/postgres_test.go
- FOUND: .planning/phases/08-high-availability/08-01-SUMMARY.md
- FOUND: commit 6889777 (test RED)
- FOUND: commit 435635c (feat GREEN)
