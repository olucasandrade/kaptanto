---
phase: 04-backfill-engine
plan: 01
subsystem: backfill
tags: [backfill, keyset-cursor, watermark, sqlite, adaptive-batch, snapshot, tdd]
dependency_graph:
  requires:
    - internal/event/event.go (ChangeEvent, IDGenerator, OpRead, OpControl)
    - internal/eventlog/badger.go (EventLog, PartitionOf)
    - modernc.org/sqlite (pure-Go SQLite, no CGO)
    - jackc/pglogrepl (ParseLSN for watermark LSN parsing)
  provides:
    - internal/backfill/backfill.go (BackfillEngine, BackfillConfig, BackfillState, MakeReadEvent, MakeControlEvent)
    - internal/backfill/cursor.go (KeysetCursor, BuildFirstQuery, BuildNextQuery)
    - internal/backfill/watermark.go (WatermarkChecker, ShouldEmit)
    - internal/backfill/optimizer.go (BatchOptimizer, Adjust, Current)
    - internal/backfill/store.go (BackfillStore interface, SQLiteBackfillStore, OpenSQLiteBackfillStore)
    - internal/eventlog.PartitionOf (exported from partitionOf)
  affects:
    - internal/eventlog/badger.go (renamed partitionOf → PartitionOf)
tech_stack:
  added:
    - internal/backfill package (new)
  patterns:
    - TDD (RED commit then GREEN commit)
    - Keyset cursor pagination (never OFFSET)
    - Watermark deduplication via EventLog.ReadPartition + PartitionOf
    - SQLite upsert via ON CONFLICT for crash-resumable state
    - Adaptive batch sizing with three-zone thresholds (fast/normal/slow/very-slow)
key_files:
  created:
    - internal/backfill/backfill.go
    - internal/backfill/cursor.go
    - internal/backfill/watermark.go
    - internal/backfill/optimizer.go
    - internal/backfill/store.go
    - internal/backfill/backfill_test.go
  modified:
    - internal/eventlog/badger.go (export PartitionOf)
decisions:
  - "PartitionOf exported as capital-P function — watermark.go needs it without circular dependency; all internal callers updated"
  - "BackfillStore is a separate SQLite file (backfill.db) — keeps backfill state separate from checkpoint store for clear ownership"
  - "MakeReadEvent and MakeControlEvent are exported package-level functions — enables black-box TDD without internal test access"
  - "snapshotTable is a stub returning nil — full pgx.Conn loop wired in Plan 04-02; engine architecture is testable without live DB"
  - "HasPendingBackfills reads LoadState without a pgx.Conn — pure state check, no DB connection needed"
metrics:
  duration: "4 min"
  completed: "2026-03-08"
  tasks_completed: 2
  files_created: 6
  files_modified: 1
---

# Phase 4 Plan 1: Backfill Engine — Core Package Summary

**One-liner:** Keyset cursor pagination, watermark deduplication, adaptive batch sizing, and SQLite crash-resumable state — all five snapshot strategies implemented and TDD-tested without a live Postgres connection.

## What Was Built

The complete `internal/backfill/` package providing the Backfill Engine skeleton:

- **cursor.go** — `KeysetCursor` with `BuildFirstQuery`/`BuildNextQuery` for single and composite primary keys. Composite PKs use row-value comparison `(pk1, pk2) > ($1, $2)`. Never emits `OFFSET`.
- **optimizer.go** — `BatchOptimizer.Adjust(d)` with three zones: fast (<1s) grows by 25%, slow (>3s or >5s) halves, normal (1s-3s) unchanged. Clamped to [100, 50000].
- **store.go** — `SQLiteBackfillStore` opens a dedicated `backfill.db` file with WAL + NORMAL synchronous mode. Upserts via `ON CONFLICT(source_id, table_name)`. `LoadState` returns nil, nil on first run.
- **watermark.go** — `WatermarkChecker.ShouldEmit` computes the target partition via `eventlog.PartitionOf(pk, numPartitions)` then scans only that partition, returning false when any entry matches (table, pk) with LSN > snapshotLSN.
- **backfill.go** — `BackfillEngine` interface, `NewEngine` constructor, strategy dispatch in `runOne`, `MakeReadEvent` (EVT-03), `MakeControlEvent` (EVT-04).
- **internal/eventlog/badger.go** — `partitionOf` renamed to `PartitionOf` (exported) so `watermark.go` can use it without a circular dependency.

## Test Coverage

24 tests across all BKF-01 through BKF-05 and EVT-03/EVT-04 requirements. All pass without a live Postgres connection using mock EventLog and SQLite temp dirs.

## Deviations from Plan

### Auto-fixed Issues

None — plan executed exactly as written. The `snapshotTable` method is deliberately stubbed (per plan spec: "full loop wired in Plan 04-02").

## Self-Check: PASSED

| Item | Status |
|------|--------|
| internal/backfill/backfill.go | FOUND |
| internal/backfill/cursor.go | FOUND |
| internal/backfill/watermark.go | FOUND |
| internal/backfill/optimizer.go | FOUND |
| internal/backfill/store.go | FOUND |
| internal/backfill/backfill_test.go | FOUND |
| test commit 45de653 | FOUND |
| impl commit f0a54d6 | FOUND |
