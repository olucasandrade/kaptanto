---
phase: 02-postgres-source-and-parser
plan: 01
subsystem: database
tags: [sqlite, checkpoint, cdc, lsn, wal, pure-go, modernc]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: Go module, event schema, CLI skeleton
provides:
  - CheckpointStore interface (Save, Load, Close) in internal/checkpoint
  - SQLiteStore backed by modernc.org/sqlite in WAL mode
  - Pure-Go durable LSN persistence for graceful shutdown and crash recovery
affects:
  - 02-postgres-connector (will call checkpoint.Open and Save/Load on every LSN advance)
  - 02-wal-parser (integrates with connector which owns the store)

# Tech tracking
tech-stack:
  added: [modernc.org/sqlite v1.46.1]
  patterns:
    - "Upsert with INSERT ON CONFLICT — idempotent checkpoint saves"
    - "TDD RED-GREEN workflow: failing tests committed before implementation"
    - "Pure-Go library selection enforced by CGO_ENABLED=0 in all test/build commands"

key-files:
  created:
    - internal/checkpoint/store.go
    - internal/checkpoint/sqlite.go
    - internal/checkpoint/sqlite_test.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "modernc.org/sqlite driver name is 'sqlite' (not 'sqlite3') — pure Go, no CGO required"
  - "Load returns ('', nil) for unknown sourceID — first-run safe, not an error condition"
  - "WAL mode + NORMAL synchronous: durability without fsync on every write; db.Close() checkpoints WAL"
  - "Open() is a constructor function on SQLiteStore, not an interface method — keeps interface lean"

patterns-established:
  - "Checkpoint store pattern: Open(path) → Store; all methods accept context.Context"
  - "sql.ErrNoRows mapped to empty string on Load — callers need not handle sql sentinel errors"

requirements-completed: [CHK-01, CHK-03, CHK-04]

# Metrics
duration: 2min
completed: 2026-03-07
---

# Phase 2 Plan 01: SQLite Checkpoint Store Summary

**Pure-Go SQLite checkpoint store using modernc.org/sqlite in WAL mode with upsert idempotency and first-run-safe Load semantics**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-07T21:15:11Z
- **Completed:** 2026-03-07T21:16:53Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 5

## Accomplishments
- CheckpointStore interface with Save/Load/Close accepting context.Context
- SQLiteStore implemented using modernc.org/sqlite — pure Go, CGO_ENABLED=0 build passes
- WAL journal mode and NORMAL synchronous pragma for crash durability without fsync overhead
- All 6 tests pass including open/save/load round-trip, idempotent upsert, first-run empty return, reopen persistence, and graceful Close

## Task Commits

Each task was committed atomically:

1. **TDD RED: failing checkpoint tests** - `04537ab` (test)
2. **TDD GREEN: SQLite checkpoint implementation** - `e5a11c3` (feat)

**Plan metadata:** (docs commit follows)

_TDD tasks have two commits (test → feat) per the TDD execution flow_

## Files Created/Modified
- `internal/checkpoint/store.go` — CheckpointStore interface
- `internal/checkpoint/sqlite.go` — SQLiteStore with WAL mode, upsert, and ErrNoRows mapping
- `internal/checkpoint/sqlite_test.go` — 6 tests covering all specified behaviours
- `go.mod` — added modernc.org/sqlite v1.46.1 and transitive deps
- `go.sum` — updated checksums

## Decisions Made
- Used `modernc.org/sqlite` (driver name `"sqlite"`) instead of `mattn/go-sqlite3` — pure Go is a hard requirement per CHK-04
- `Load` returns `("", nil)` for unknown sourceID — connectors on first run get empty string without error handling
- WAL mode via DSN pragma `_pragma=journal_mode(WAL)` — `db.Close()` triggers WAL checkpoint, satisfying CHK-03 (graceful shutdown flush)
- `Open()` is a package-level constructor, not an interface method — keeps `CheckpointStore` interface lean for future alternative implementations

## Deviations from Plan

None — plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None — no external service configuration required.

## Next Phase Readiness
- Checkpoint store is ready for the Postgres connector (02-02) to call `Open`, `Save`, and `Load` on every LSN advance
- The `CheckpointStore` interface is the integration point: connector will hold a `checkpoint.CheckpointStore` field
- Pure-Go constraint maintained: `CGO_ENABLED=0 go build ./...` succeeds

---
*Phase: 02-postgres-source-and-parser*
*Completed: 2026-03-07*
