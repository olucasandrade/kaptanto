---
phase: 02-postgres-source-and-parser
plan: 03
subsystem: source
tags: [go, postgres, replication, pglogrepl, pgconn, cdc, wal, connector, backoff, checkpoint]

# Dependency graph
requires:
  - 02-01: CheckpointStore interface (Save/Load/Close) used by connector
  - 02-02: pgoutput.Parser (Parse, ClearRelationCache) called per XLogData message
provides:
  - PostgresConnector with full replication loop at internal/source/postgres
  - Config with ApplyDefaults, BuildReplicationDSN, EvalSlotCheck (pure logic helpers)
  - ensureSlot / ensurePublication auto-setup helpers
  - checkReplicaIdentity / checkWALLag / checkPrimary validation helpers
  - ClearRelationCache() method added to pgoutput.Parser
affects: [03-event-log, 04-backfill, cmd/kaptanto main wiring]

# Tech tracking
tech-stack:
  added:
    - github.com/jackc/pgconn v1.14.3 (transitive via pgx/v5)
    - github.com/jackc/pgx/v5/pgconn (used directly for replication connection)
    - github.com/jackc/pgx/v5/pgproto3 (CopyData message parsing)
  patterns:
    - "Two-connection pattern: replConn (*pgconn.PgConn) for WAL; queryConn (*pgx.Conn) for SQL"
    - "Heartbeat via context.WithDeadline + pgconn.Timeout(err) detection"
    - "CHK-01: store.Save before SendStandbyStatusUpdate, comment co-located with code"
    - "EvalSlotCheck pure function for SRC-06 logic — testable without DB"
    - "BuildReplicationDSN handles ? vs & separator for DSN parameter appending"
    - "Exponential backoff: backoff = min(backoff*2, MaxBackoff) in reconnect loop"

key-files:
  created:
    - internal/source/postgres/connector.go
    - internal/source/postgres/publication.go
    - internal/source/postgres/validation.go
    - internal/source/postgres/connector_test.go
  modified:
    - internal/parser/pgoutput/parser.go (added ClearRelationCache)
    - go.mod
    - go.sum

key-decisions:
  - "pgx/v5/pgconn is the correct import for replication connections — pglogrepl uses this exact package (not standalone jackc/pgconn)"
  - "Commit detection via WALData[0] == 'C' (pglogrepl.MessageTypeCommit) inside XLogData handler — store.Save precedes SendStandbyStatusUpdate"
  - "ensureSlot uses replConn (*pgconn.PgConn) for CreateReplicationSlot; queryConn (*pgx.Conn) for pg_replication_slots check"
  - "ClearRelationCache added to parser as a Rule 1 fix — it was in the interface spec but not implemented in 02-02"
  - "EvalSlotCheck exported as pure function to enable unit testing of SRC-06 logic without live DB"

patterns-established:
  - "PostgresConnector.Run → connectAndStream → receiveLoop: three-layer structure for reconnect/setup/stream"
  - "Graceful shutdown: ctx.Err() != nil after connectAndStream returns → propagate ctx.Err() immediately"
  - "WAL lag goroutine uses sub-context (lagCtx) cancelled via defer cancelLag() when receiveLoop exits"

requirements-completed: [SRC-01, SRC-02, SRC-03, SRC-04, SRC-05, SRC-06, SRC-07, SRC-08]

# Metrics
duration: 6min
completed: 2026-03-08
---

# Phase 2 Plan 03: PostgresConnector Summary

**PostgresConnector with full WAL replication loop, exponential backoff reconnection, slot/publication auto-setup, REPLICA IDENTITY validation, WAL lag monitoring, and CHK-01 safe checkpoint ordering**

## Performance

- **Duration:** ~6 min
- **Started:** 2026-03-08T03:18:30Z
- **Completed:** 2026-03-08
- **Tasks:** 2 (Task 1: TDD + helpers; Task 2: replication loop)
- **Files created:** 4, **Files modified:** 3

## Accomplishments

- `PostgresConnector` with `New`, `Run`, `Events` API
- `Config` struct with `ApplyDefaults` — zero-value safe defaults for all fields
- `connectAndStream` — opens two connections, verifies primary, checks REPLICA IDENTITY, creates slot/publication, loads checkpoint, starts replication
- `receiveLoop` — `context.WithDeadline`-based heartbeat, `pgconn.Timeout` detection, `PrimaryKeepaliveMessage` handling, `XLogData` decode via parser, Commit detection for CHK-01 checkpoint save
- `ensurePublication` — idempotent CREATE PUBLICATION
- `ensureSlot` — idempotent slot creation with SRC-06 `needsSnapshot` detection
- `checkReplicaIdentity` — pg_class query, `slog.Warn` on default identity
- `checkWALLag` — pg_stat_replication query, `slog.Warn` on threshold
- `checkPrimary` — `pg_is_in_recovery()` guard
- `ClearRelationCache` added to `pgoutput.Parser`
- All tests pass with `CGO_ENABLED=0`; `make verify-no-cgo` passes

## Task Commits

1. **RED: Failing connector unit tests** — `d47d3d4` (test)
2. **GREEN: Full connector implementation** — `95a5c0a` (feat)

## Decisions Made

- Used `github.com/jackc/pgx/v5/pgconn` (not standalone `jackc/pgconn`) for the replication connection — pglogrepl's function signatures require this exact package.
- Commit detection uses `xld.WALData[0] == 'C'` (pglogrepl.MessageTypeCommit = 'C') inside the XLogData handler. This is correct because pgoutput sends Commit messages as XLogData records in the WAL stream.
- `EvalSlotCheck` exported as a pure function — allows unit testing of the SRC-06 slot-absent-after-failover detection without requiring a live Postgres connection.
- `ClearRelationCache()` was specified in the plan's interface section but missing from 02-02's implementation. Added as a Rule 1 auto-fix (missing method required by this plan's connector).
- Added `standalone jackc/pgconn` dependency was removed in favor of using `pgx/v5/pgconn` which is already a transitive dependency.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Added ClearRelationCache to pgoutput.Parser**
- **Found during:** Task 1 (interface review)
- **Issue:** Plan 02-02 interface specified `ClearRelationCache()` but the method was absent from parser.go
- **Fix:** Added `ClearRelationCache()` method resetting `p.relations` to a new `RelationCache`
- **Files modified:** `internal/parser/pgoutput/parser.go`
- **Commit:** `d47d3d4`

**2. [Rule 3 - Blocking] Switched from standalone jackc/pgconn to pgx/v5/pgconn**
- **Found during:** Task 2 (build errors)
- **Issue:** `pglogrepl.StartReplication`, `pglogrepl.SendStandbyStatusUpdate` require `*pgx/v5/pgconn.PgConn`, not `*jackc/pgconn.PgConn`
- **Fix:** Removed standalone `pgconn` import; used `github.com/jackc/pgx/v5/pgconn` and `pgproto3` directly
- **Files modified:** `internal/source/postgres/connector.go`, `internal/source/postgres/publication.go`
- **Commit:** `95a5c0a`

## Self-Check: PASSED

- FOUND: internal/source/postgres/connector.go
- FOUND: internal/source/postgres/publication.go
- FOUND: internal/source/postgres/validation.go
- FOUND: internal/source/postgres/connector_test.go
- FOUND commit: d47d3d4 (test RED)
- FOUND commit: 95a5c0a (feat GREEN)
