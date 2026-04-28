---
phase: 15-distributed-event-log
plan: "01"
subsystem: eventlog
tags: [nats, jetstream, raft, event-log, cdc, distributed, go]

requires:
  - phase: 14-shared-state-foundation
    provides: PostgresCursorStore and cluster wiring in root.go that NatsEventLog will plug into

provides:
  - NatsEventLog struct implementing the EventLog interface (Append, AppendBatch, ReadPartition, Close, Ping)
  - startEmbeddedNATS helper for in-process NATS server lifecycle
  - NatsServerConfig and NatsEventLogConfig types for cluster configuration
  - Unit test suite for NatsEventLog (5 tests, CGO_ENABLED=0, ~7s)
  - NATS JetStream dependencies: nats-server/v2 v2.12.8, nats.go v1.51.0

affects:
  - 15-02 (CLI wiring — must open NatsEventLog in cluster branch of root.go)
  - 16-partition-handoff (reads events via EventLog interface — NatsEventLog compatible)
  - observability (Ping() available for /healthz health check)

tech-stack:
  added:
    - github.com/nats-io/nats-server/v2 v2.12.8 (embedded NATS server with JetStream)
    - github.com/nats-io/nats.go v1.51.0 (JetStream client API)
  patterns:
    - Embedded NATS server started in-process (no subprocess) using nats-server/v2/server import
    - Stream Replicas=max(1, peers+1) — R=1 for single-node, R=3 for 3-node cluster
    - SyncAlways is a top-level Options field (NOT inside JetStreamConfig sub-struct)
    - Duplicate detection via PubAck.Duplicate=true (NOT error) — seq=0 sentinel returned
    - StreamConfig.Duplicates=retention matches Badger's persistent dedup semantics
    - OrderedConsumer with DeliverByStartSequencePolicy for ReadPartition cursor resume
    - TDD: test helper uses PartitionOf directly to avoid scanning all 64 partitions

key-files:
  created:
    - internal/eventlog/nats.go
    - internal/eventlog/nats_server.go
    - internal/eventlog/nats_test.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "NatsEventLog.Append returns seq=0 for duplicates (PubAck.Duplicate=true) matching BadgerEventLog's LOG-03 sentinel — no behavior change for callers"
  - "Stream Replicas=max(1, len(peers)+1) avoids single-node stream creation failure while supporting 3-node cluster with R=3"
  - "StreamConfig.Duplicates=retention (not default 2m) to prevent WAL re-delivery after crash creating duplicates when recovery takes longer than 2 minutes"
  - "FetchMaxWait(2s) in ReadPartition — acceptable latency for cursor resume; tests use PartitionOf directly to avoid 64×2s scan timeout"
  - "AppendBatch is a sequential loop over Append (no native NATS batch transaction) — CHK-01 safe because each Append blocks until PubAck"

patterns-established:
  - "NATS embedded server: startEmbeddedNATS sets SyncAlways at top-level Options, sets Cluster block only when peers configured"
  - "JetStream dedup: always check ack.Duplicate before returning seq — never check err.Error() for duplicate string"
  - "Partition subjects: kaptanto.events.{partition:05d} zero-padded for lexicographic ordering under wildcard filter"

requirements-completed: [EVLOG-01, EVLOG-02]

duration: 8min
completed: "2026-04-28"
---

# Phase 15 Plan 01: NatsEventLog Implementation Summary

**NATS JetStream-backed EventLog with Raft-replicated durability — embedded in-process server, synchronous PubAck (CHK-01), seq=0 duplicate sentinel, 64-partition OrderedConsumer reads**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-04-28T00:14:02Z
- **Completed:** 2026-04-28T00:21:51Z
- **Tasks:** 2
- **Files modified:** 5 (go.mod, go.sum, nats.go, nats_server.go, nats_test.go)

## Accomplishments

- Added NATS JetStream dependencies (nats-server/v2 v2.12.8, nats.go v1.51.0) — CGO_ENABLED=0 and make verify-no-cgo both pass confirming no CGO leakage from transitive go-tpm dependency
- Implemented NatsEventLog satisfying the EventLog interface with synchronous Append (CHK-01), seq=0 duplicate sentinel (LOG-03), sequential AppendBatch, OrderedConsumer-based ReadPartition, and Ping for /healthz
- All 5 TestNats* unit tests pass with CGO_ENABLED=0 in ~7s; full make test suite passes (11/11 eventlog tests, 0 failures across all packages)

## Task Commits

Each task was committed atomically:

1. **Task 1: Add NATS dependencies to go.mod** - `407a7fd` (chore)
2. **Task 2: RED — failing tests for NatsEventLog** - `662059e` (test)
3. **Task 2: GREEN — implement NatsEventLog and fix tests** - `911ae3f` (feat)

**Plan metadata:** (docs commit follows)

_Note: Task 2 is a TDD task — test commit precedes implementation commit._

## Files Created/Modified

- `internal/eventlog/nats_server.go` — startEmbeddedNATS helper; NatsServerConfig struct with SyncAlways (top-level Options field), ClusterOpts, Routes; blocks until ReadyForConnections
- `internal/eventlog/nats.go` — NatsEventLog struct; OpenNats constructor with dynamic Replicas; Append (synchronous PublishMsg + ack.Duplicate check); AppendBatch (sequential loop); ReadPartition (OrderedConsumer + DeliverByStartSequencePolicy); Close; Ping; compile-time `var _ EventLog = (*NatsEventLog)(nil)` assertion
- `internal/eventlog/nats_test.go` — 5 unit tests using openTestNatsEventLog (R=1, SyncAlways=false, single-node); tests use PartitionOf to avoid 64×FetchMaxWait scanning timeout
- `go.mod` / `go.sum` — nats-server/v2 and nats.go added with transitive deps

## Decisions Made

- **R=max(1, peers+1):** Hard-coding R=3 would fail stream creation on a single-node deployment. Dynamic replica count based on configured peers makes OpenNats work in both single-node and 3-node cluster configurations.
- **StreamConfig.Duplicates=retention:** The default 2-minute dedup window is too short for crash-recovery scenarios where WAL reconnect may take longer. Setting it to the same retention duration as Badger's persistent dedup index preserves idempotency guarantees (Pitfall 2 from research).
- **FetchMaxWait(2s):** ReadPartition blocks up to 2 seconds waiting for messages. Tests use PartitionOf directly to target the exact partition — avoiding the 64×2s = 128s scan that caused the original TestNatsEventLogReadPartition timeout.
- **AppendBatch as sequential loop:** JetStream has no native multi-subject atomic batch. Sequential Append calls are CHK-01 safe since each blocks until PubAck. The alternative (async publish + collect acks) adds complexity without correctness benefit.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Test scan-all-64-partitions pattern caused 2-minute timeout**
- **Found during:** Task 2 (TestNatsEventLogReadPartition first run)
- **Issue:** Original test design scanned all 64 partitions using ReadPartition, each with a FetchMaxWait(2s) timeout. For 63 empty partitions this totals 126s, exceeding the default 2m test timeout.
- **Fix:** Updated TestNatsEventLogReadPartition and TestNatsEventLogPartitionIsolation to call `eventlog.PartitionOf(key, 64)` directly and read only the correct partition, matching how production callers work.
- **Files modified:** internal/eventlog/nats_test.go
- **Verification:** All 5 TestNats* tests complete in ~7s
- **Committed in:** 911ae3f (Task 2 feat commit)

---

**Total deviations:** 1 auto-fixed (Rule 1 — bug in test design)
**Impact on plan:** Fix was necessary for test correctness. No scope creep. The PartitionOf-based approach is the correct pattern for tests and mirrors production caller behavior.

## Issues Encountered

- First test run timed out due to 64-partition scan strategy. Identified and fixed in the same implementation pass (see Deviations above).

## User Setup Required

None — no external service configuration required. The embedded NATS server starts in-process; no external NATS daemon is needed for single-node or test mode.

## Next Phase Readiness

- NatsEventLog is complete and implements the full EventLog interface
- Phase 15-02 must wire NatsEventLog into root.go's cluster branch (replacing BadgerEventLog when --cluster is set) and add the --cluster-peers / --nats-cluster-port CLI flags
- The Ping() method is ready for /healthz integration when the observability layer is updated
- WatermarkChecker compatibility confirmed: PartitionOf function is shared — BKF-02 invariant preserved

---
*Phase: 15-distributed-event-log*
*Completed: 2026-04-28*

## Self-Check: PASSED

- internal/eventlog/nats.go: FOUND
- internal/eventlog/nats_server.go: FOUND
- internal/eventlog/nats_test.go: FOUND
- .planning/phases/15-distributed-event-log/15-01-SUMMARY.md: FOUND
- Commit 407a7fd (chore - NATS deps): FOUND
- Commit 662059e (test - failing tests): FOUND
- Commit 911ae3f (feat - NatsEventLog): FOUND
