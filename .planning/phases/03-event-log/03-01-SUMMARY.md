---
phase: 03-event-log
plan: 01
subsystem: database
tags: [badger, event-log, cdc, partitioning, deduplication, ttl, fnv, binary-encoding]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: ChangeEvent type (internal/event/event.go) used as the value stored in Badger
  - phase: 02-postgres-source-and-parser
    provides: CHK-01 invariant contract — Append must be called before store.Save (already wired in connector)

provides:
  - BadgerEventLog: durable, partitioned, deduplicated, TTL-expiring append-only event store
  - EventLog interface: Append / ReadPartition / Close
  - LogEntry type: Seq + Event pair returned by ReadPartition
  - Fixed-width big-endian key encoding for sort-correct lexicographic range scans
  - FNV-1a partitioning by primary key bytes (deterministic, 64 partitions default)
  - Idempotent dedup index keyed by IdempotencyKey (same-TTL as partition entry)

affects:
  - 03-02 (backfill watermark check will call ReadPartition to detect duplicate snapshot rows)
  - 04-router (partitioned router will call ReadPartition to fan out events to outputs)
  - connector wiring (connector.go will call eventlog.Append before store.Save)

# Tech tracking
tech-stack:
  added:
    - github.com/dgraph-io/badger/v4 v4.9.1 — embedded LSM key-value store with native TTL
    - github.com/dgraph-io/ristretto/v2 v2.2.0 — Badger's cache layer (transitive dep)
    - go.opentelemetry.io/otel v1.37.0 — Badger telemetry (transitive dep)
    - google.golang.org/protobuf v1.36.7 — Badger serialization (transitive dep)
  patterns:
    - Fixed-width big-endian binary keys for sort-correct lexicographic range scans (never decimal ASCII)
    - Separate key prefix namespacing: 0x50 for partition entries, 0x44 for dedup entries
    - Badger Sequence per partition pre-advanced past 0 so seq=0 is unambiguous duplicate sentinel
    - Same TTL on both partition entry and dedup entry (pitfall 4 — dedup must not expire before the entry it guards)
    - seq.Next() called OUTSIDE db.Update transaction (reduces MVCC conflict window)
    - WithLogger(nil) on BadgerDB options (suppress noise, use slog project-wide)
    - seq.Release() before db.Close() on graceful shutdown (flush leased integers)

key-files:
  created:
    - internal/eventlog/eventlog.go — EventLog interface + LogEntry type
    - internal/eventlog/keys.go — encodePartKey, encodePartPrefix, encodeDedupKey, encodePartSeq, decodePartKey
    - internal/eventlog/badger.go — BadgerEventLog implementation (Open, Append, ReadPartition, Close, partitionOf)
    - internal/eventlog/badger_test.go — TDD tests covering LOG-01 through LOG-04
  modified:
    - go.mod — added badger/v4 and transitive deps
    - go.sum — updated hashes

key-decisions:
  - "Badger sequences pre-advanced past 0 at Open time — ensures seq=0 is unambiguous duplicate-detected sentinel; first real Append always returns seq >= 1"
  - "seq.Next() called OUTSIDE db.Update transaction — reduces MVCC read set, avoids sequence lock inside transaction window; gap in sequence on crash is acceptable"
  - "Fixed-width big-endian binary for all numeric key components — decimal ASCII breaks lexicographic sort (p:0:s:10 before p:0:s:9)"
  - "Same retention TTL on partition entry and dedup entry — dedup index must not outlive or die before the entry it guards"
  - "WithLogger(nil) on BadgerDB — project uses slog; Badger's default logger would interleave unstructured output"
  - "seq.Release() before db.Close() on Close() — best-effort flush of leased integers, reduces sequence waste across restarts"

patterns-established:
  - "Partition key layout: [0x50][partition 4B BE][0x53][seq 8B BE] — 14 bytes, all sort-correct"
  - "Dedup key layout: [0x44][idempotency_key bytes] — variable length, distinct prefix from partition keys"
  - "Dedup value: [partition 4B BE][seq 8B BE] — enables reverse lookup of partition+seq from idempotency key"

requirements-completed: [LOG-01, LOG-02, LOG-03, LOG-04]

# Metrics
duration: 15min
completed: 2026-03-08
---

# Phase 3 Plan 1: BadgerEventLog — Durable Partitioned Event Store Summary

**BadgerDB-backed append-only event log with FNV-1a partitioning, idempotent dedup by IdempotencyKey, and automatic TTL expiry — satisfying LOG-01 through LOG-04**

## Performance

- **Duration:** ~15 min
- **Started:** 2026-03-08T03:50:13Z
- **Completed:** 2026-03-08T04:05:00Z
- **Tasks:** 2 (RED + GREEN — TDD plan)
- **Files modified:** 6

## Accomplishments

- Implemented the `EventLog` interface and `LogEntry` type in `internal/eventlog/eventlog.go`
- Implemented fixed-width big-endian key encoding in `internal/eventlog/keys.go` (14-byte partition keys, variable dedup keys, 12-byte dedup values) — never decimal ASCII
- Implemented `BadgerEventLog` with FNV-1a partitioning, single-transaction idempotent dedup, same-TTL on both entries, and per-partition Badger sequences
- All 6 TDD tests pass: Append+Read, Dedup, Partitioning determinism, TTL expiry, ReadPartition fromSeq range scan, Close

## Task Commits

1. **RED: Failing tests** - `13f2a30` (test) — 6 tests written against unimplemented interface
2. **GREEN: Implementation** - `7fcd1ea` (feat) — eventlog.go, keys.go, badger.go; [Rule 1] sequence-zero fix; [Rule 3] go.sum fix

## Files Created/Modified

- `internal/eventlog/eventlog.go` — EventLog interface + LogEntry struct
- `internal/eventlog/keys.go` — encodePartKey, encodePartPrefix, encodeDedupKey, encodePartSeq, decodePartKey
- `internal/eventlog/badger.go` — BadgerEventLog (Open, Append, ReadPartition, Close, partitionOf)
- `internal/eventlog/badger_test.go` — TDD tests (6 tests, all passing)
- `go.mod` — added badger/v4 and transitive deps
- `go.sum` — updated hashes

## Decisions Made

- **Sequence sentinel:** Badger sequences start at 0. We pre-advance past 0 in `Open()` so `seq=0` is an unambiguous "duplicate detected" sentinel. This satisfies the plan's requirement that first Append returns seq > 0.
- **seq.Next() outside transaction:** Called before `db.Update` to reduce the MVCC read set. A crash between `Next()` and `SetEntry` wastes one sequence number — acceptable since sequences need not be gapless.
- **Same TTL for partition + dedup entries:** Critical correctness requirement. If dedup entry expires before the partition entry, a re-delivered duplicate would be re-written.
- **WithLogger(nil):** Suppresses Badger's internal logs; kaptanto uses structured slog.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed Badger sequence starting at 0, conflicting with duplicate sentinel**
- **Found during:** GREEN phase (running tests post-implementation)
- **Issue:** `badger.Sequence.Next()` returns 0 for the first call. The plan uses `seq=0` as the "duplicate detected" sentinel, but without intervention, the first legitimate Append also returns 0, making the two indistinguishable.
- **Fix:** In `Open()`, call `seq.Next()` once per partition immediately after `db.GetSequence()` to consume 0. First real `Append` then returns seq >= 1.
- **Files modified:** `internal/eventlog/badger.go`
- **Verification:** All 6 tests pass including `TestBadgerEventLog_AppendAndRead` asserting `seq > 0`
- **Committed in:** 7fcd1ea (GREEN phase commit)

**2. [Rule 3 - Blocking] Fixed missing go.sum entries after badger upgraded crypto/text packages**
- **Found during:** GREEN phase (`go build ./...` after implementation)
- **Issue:** `go get github.com/dgraph-io/badger/v4@v4.9.1` upgraded `golang.org/x/crypto` and `golang.org/x/text`, which added new sub-packages to the dependency graph not yet in go.sum.
- **Fix:** Ran `go get github.com/jackc/pgx/v5/pgconn@v5.5.4` to regenerate correct go.sum entries.
- **Files modified:** `go.mod`, `go.sum`
- **Verification:** `CGO_ENABLED=0 go build ./...` exits 0
- **Committed in:** 7fcd1ea (GREEN phase commit)

---

**Total deviations:** 2 auto-fixed (1 bug, 1 blocking)
**Impact on plan:** Both fixes required for correctness. No scope creep.

## Issues Encountered

None beyond the auto-fixed deviations above.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- `internal/eventlog` package is complete and ready for integration
- Phase 3 Plan 2 (backfill watermark) will call `ReadPartition` to detect duplicate snapshot rows
- Phase 4 router will call `ReadPartition` to fan out events to output servers
- The `PostgresConnector` in `internal/source/postgres/connector.go` is ready to accept an `EventLog` field and call `Append` before `store.Save` (the CHK-01 integration point described in the research doc)

---
*Phase: 03-event-log*
*Completed: 2026-03-08*
