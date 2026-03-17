---
phase: 09-mongodb-connector
plan: "01"
subsystem: mongodb-source
tags: [mongodb, change-streams, cdc, checkpoint, chk-01, tdd]
dependency_graph:
  requires:
    - internal/checkpoint/store.go
    - internal/eventlog/eventlog.go
    - internal/event/event.go
  provides:
    - internal/source/mongodb/connector.go
    - internal/source/mongodb/connector_test.go
  affects:
    - cmd/kaptanto (future wiring in runPipeline)
tech_stack:
  added:
    - go.mongodb.org/mongo-driver/v2 v2.5.0
  patterns:
    - WatchFn injection for test isolation (mirrors Postgres pgconn injection)
    - CHK-01 invariant: Append before Save token
    - Drain-or-drop channel send (matching Phase 07.3 pattern)
    - Post-construction watchFn setup via NewWithWatchFn
key_files:
  created:
    - internal/source/mongodb/connector.go
    - internal/source/mongodb/connector_test.go
  modified:
    - go.mod
    - go.sum
decisions:
  - "[09-01] WatchFn type injected via NewWithWatchFn — enables unit tests without a real MongoDB; production connector lazily builds real watchFn in Run"
  - "[09-01] resumeToken loaded at construction time (not at Run time) — Load happens once in constructor; Run goroutines share the same initial token"
  - "[09-01] NeedsSnapshot guarded by sync.Mutex — multiple collection goroutines may set it concurrently; mutex prevents data race"
  - "[09-01] normalizeStub in Plan 01 — minimal operationType-to-Operation mapping, replaced by parser/mongodb.NormalizeChangeEvent in Plan 02"
  - "[09-01] AppendAndQueue takes explicit bson.Raw token parameter — unlike Postgres (token embedded in commit), MongoDB resume token arrives per-event; caller passes it explicitly"
  - "[09-01] CommandError code 260 = InvalidResumeToken — driver v2 embeds this in mongo.CommandError; fallback strings.Contains handles wrapped errors"
metrics:
  duration: "3 min"
  completed: "2026-03-17"
  tasks: 1
  files: 4
---

# Phase 9 Plan 1: MongoDBConnector — Change Stream Loop Summary

**One-liner:** MongoDB Change Stream connector with resume token persistence, InvalidResumeToken detection, and CHK-01-enforced checkpoint ordering using WatchFn injection for test isolation.

## What Was Built

`internal/source/mongodb/connector.go` implements `MongoDBConnector` with:

- `Config.ApplyDefaults()`: SourceID defaults to `"mongo_default"`; Database validation returns error if blank
- `New` / `NewWithEventLog` / `NewWithWatchFn` constructors following the Postgres connector pattern
- `Run(ctx)`: per-collection goroutines with exponential backoff (2s → 60s), context-aware shutdown
- `AppendAndQueue(ctx, ev, token)`: CHK-01 invariant enforced — `el.Append` must succeed before `store.Save`
- `NeedsSnapshot()`: returns true after `InvalidResumeToken` error (code 260 or string match)
- `Events() <-chan *event.ChangeEvent`: drain-or-drop channel (matches Phase 07.3 pattern)
- `normalizeStub`: minimal Plan 01 normalizer stub (Plan 02 replaces with full BSON normalizer)
- `ChangeStreamIter` interface + `WatchFn` type for test injection

`internal/source/mongodb/connector_test.go`: 11 unit tests covering:
- Config defaults and validation
- Constructor event log wiring
- CHK-01: Append failure prevents token save
- Resume token loaded from store and passed to watchFn
- InvalidResumeToken detection sets NeedsSnapshot=true
- Context cancellation returns `context.Canceled`

## TDD Execution

**RED commit:** `d83d8d2` — test(09-01): add failing tests for MongoDBConnector
**GREEN commit:** `8289a09` — feat(09-01): implement MongoDBConnector with Change Stream loop

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] fakeEventLog.ReadPartition return type mismatch**
- **Found during:** GREEN phase (first test run)
- **Issue:** Test's `fakeEventLog.ReadPartition` returned `[]interface{}` but `eventlog.EventLog` interface requires `[]eventlog.LogEntry`
- **Fix:** Updated test import and return type to use `eventlog.LogEntry`
- **Files modified:** `internal/source/mongodb/connector_test.go`

**2. [Rule 1 - Bug] Error message case mismatch**
- **Found during:** First GREEN test run
- **Issue:** Test checked `err.Error()` for `"database"` (lowercase); implementation returned `"Config.Database"` (capital D)
- **Fix:** Changed error message to `"mongodb: database is required"` to match test expectation
- **Files modified:** `internal/source/mongodb/connector.go`

## Deferred Items

`internal/parser/mongodb/normalizer_test.go` exists (pre-committed for Plan 09-02) and imports `go.mongodb.org/mongo-driver/v2/bson/primitive` which does not exist in the v2 driver (primitive types moved into `bson` directly). This will cause `go test ./...` to fail at the parser/mongodb package. Deferred to Plan 09-02 which implements the normalizer — the fix is replacing `primitive` imports with `bson` equivalents.

## Self-Check: PASSED
