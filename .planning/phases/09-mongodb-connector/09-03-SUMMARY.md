---
phase: 09-mongodb-connector
plan: "03"
subsystem: source/mongodb, config, cmd
tags: [mongodb, snapshot, watermark, pipeline, cdc, srm-12]
dependency_graph:
  requires: [09-01, 09-02]
  provides: [runMongoPipeline, MongoSnapshot, SourceType, normalizer-integration]
  affects: [internal/cmd/root.go, internal/config/config.go, internal/source/mongodb/connector.go]
tech_stack:
  added: []
  patterns:
    - WatermarkChecker interface injection for testability
    - findFn/snapshotLSN injection for unit testing without live MongoDB
    - SourceType() DSN-prefix auto-detection (no config flag required)
    - Single re-snapshot attempt on InvalidResumeToken with restart
key_files:
  created:
    - internal/source/mongodb/snapshot.go
    - internal/source/mongodb/snapshot_test.go
  modified:
    - internal/source/mongodb/connector.go
    - internal/config/config.go
    - internal/config/config_test.go
    - internal/cmd/root.go
    - internal/cmd/root_test.go
decisions:
  - "[09-03]: WatermarkChecker defined as a local interface in snapshot.go — *backfill.WatermarkChecker satisfies it; avoids import cycle between source/mongodb and backfill"
  - "[09-03]: findFn injection on MongoSnapshot with ...any opts — allows test bypass without real MongoDB client"
  - "[09-03]: normalizeSnapshotDoc builds synthetic change stream wrapper around plain BSON doc — reuses NormalizeChangeEvent without duplicating field-mapping logic"
  - "[09-03]: SourceType() on Config struct (method, not package-level func) — consistent with Go pattern, no global state"
  - "[09-03]: serverSelectionTimeoutMS=500 in TestMongoDBFlagRoute URI — makes driver fail fast on unreachable host; test completes in <1s"
  - "[09-03]: normalizeStub removed from connector.go — replaced by mongoparser.NormalizeChangeEvent (Plan 02 real normalizer)"
metrics:
  duration_seconds: 530
  completed_date: "2026-03-17"
  tasks_completed: 2
  files_changed: 7
---

# Phase 9 Plan 03: MongoDB Pipeline Wiring and Snapshot Summary

MongoDB pipeline integration complete: MongoSnapshot with WatermarkChecker gating, SourceType auto-detection from DSN prefix, and runMongoPipeline dispatched from runPipeline — no stub remains.

## Tasks Completed

| # | Name | Commit | Key Files |
|---|------|--------|-----------|
| 1 | MongoDB snapshot with watermark coordination | 9bf8658 | internal/source/mongodb/snapshot.go, snapshot_test.go |
| 2 | Wire MongoDB pipeline into runPipeline and update config SourceType | e4ef897 | internal/config/config.go, internal/cmd/root.go, connector.go |

## What Was Built

### Task 1: MongoSnapshot (snapshot.go)

`MongoSnapshot.Run` iterates all configured collections:
- Documents fetched via `findFn` (or real `mongo.Collection.Find`) sorted by `_id` — keyset cursor, never OFFSET (CLAUDE.md invariant 3)
- Each document converted to `OpRead` via `normalizeSnapshotDoc`, which builds a synthetic change stream wrapper around the plain BSON doc and calls `mongoparser.NormalizeChangeEvent` to reuse the existing normalizer
- `WatermarkChecker.ShouldEmit(ctx, coll, key, snapshotLSN)` gates each row before `appendFn` is called — rows returning false are silently skipped (SRC-12)
- After all rows for a collection, an `OpControl` event with `metadata["event"]="snapshot_complete"` is appended (matching Postgres backfill pattern)
- `SetFindFn` and `SetSnapshotLSN` injection points enable unit testing without a live MongoDB instance

### Task 2: SourceType + runMongoPipeline

- `config.Config.SourceType()` returns `"mongodb"` for `mongodb://` and `mongodb+srv://` DSN prefixes; `"postgres"` otherwise — auto-detection with no new config flag
- `runPipeline` now dispatches to `runMongoPipeline` when `cfg.SourceType() == "mongodb"`, leaving the Postgres path 100% unchanged
- `runMongoPipeline`: creates `MongoDBConnector` via `NewWithEventLog`, runs errgroup with connector + router + outputServer + cursorStore; after `g.Wait()`, if `connector.NeedsSnapshot()` triggers a `MongoSnapshot` run then restarts a new connector in a second errgroup
- `extractDBFromMongoURI` helper parses database name from URI path component
- `connector.go`: `normalizeStub` removed; `consumeStream` now calls `mongoparser.NormalizeChangeEvent` directly (Plan 02 normalizer integration complete)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Design] WatermarkChecker as local interface in snapshot.go**

- **Found during:** Task 1
- **Issue:** Importing `*backfill.WatermarkChecker` directly in `internal/source/mongodb/snapshot.go` creates a cross-package coupling. The plan specified using `*backfill.WatermarkChecker` but as a local interface the type is satisfied without coupling.
- **Fix:** Defined `WatermarkChecker` interface in `snapshot.go` with single `ShouldEmit` method — `*backfill.WatermarkChecker` satisfies it automatically via structural typing.
- **Files modified:** internal/source/mongodb/snapshot.go

**2. [Rule 1 - Bug] fakeSnapshotEventLog had Ping() method not in EventLog interface**

- **Found during:** Task 1 code review
- **Issue:** Test helper had `Ping() error` method which is not part of the `eventlog.EventLog` interface.
- **Fix:** Removed the extraneous method.
- **Files modified:** internal/source/mongodb/snapshot_test.go

**3. [Rule 2 - Critical] TestMongoDBFlagRoute needed serverSelectionTimeoutMS**

- **Found during:** Task 2 — test hung for 30+ seconds
- **Issue:** MongoDB driver's default server selection timeout is 30s; test with unreachable URI blocked.
- **Fix:** Added `?serverSelectionTimeoutMS=500` to the test URI — driver fails in <600ms; test completes in <1s.
- **Files modified:** internal/cmd/root_test.go

## Verification

```
go build ./...   ✓ clean
go test ./...    ✓ all packages pass (integration tests skip gracefully without DSN)
```

Success criteria met:
- SRC-12: MongoDB re-snapshot uses `WatermarkChecker.ShouldEmit` to prevent duplicate events
- Pipeline auto-detects MongoDB vs Postgres from DSN prefix — no config flag required
- `NormalizeChangeEvent` (Plan 02) called from `consumeStream` — `normalizeStub` removed
- Snapshot emits `OpControl` snapshot_complete event per collection
- `go build ./...` clean; existing tests unbroken

## Self-Check

### Files Created/Modified

- [x] internal/source/mongodb/snapshot.go — exists
- [x] internal/source/mongodb/snapshot_test.go — exists
- [x] internal/source/mongodb/connector.go — modified (normalizeStub removed)
- [x] internal/config/config.go — SourceType() added
- [x] internal/config/config_test.go — TestSourceType_* added
- [x] internal/cmd/root.go — runMongoPipeline + MongoDB dispatch added
- [x] internal/cmd/root_test.go — TestMongoDBFlagRoute added

### Commits

- [x] 9bf8658 — feat(09-03): implement MongoSnapshot with watermark coordination
- [x] e4ef897 — feat(09-03): wire MongoDB pipeline and add SourceType auto-detection

## Self-Check: PASSED
