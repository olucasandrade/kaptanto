---
phase: 09-mongodb-connector
plan: "02"
subsystem: database
tags: [mongodb, bson, change-streams, normalizer, extended-json, cdc]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: event.ChangeEvent, event.IDGenerator (unified event type)
  - phase: 09-mongodb-connector-01
    provides: MongoDB Change Stream source infrastructure
provides:
  - NormalizeChangeEvent(raw bson.Raw, sourceID string, idGen *event.IDGenerator) (*event.ChangeEvent, error)
  - BSON-to-extended-JSON serialization for Key/Before/After fields
  - Full operation type mapping: insert/update/delete/replace → event.Operation
affects:
  - 09-mongodb-connector-03 (stream consumer wiring)
  - any pipeline phase that routes MongoDB events

# Tech tracking
tech-stack:
  added:
    - go.mongodb.org/mongo-driver/v2 v2.5.0 (MongoDB driver with bson package)
  patterns:
    - TDD: test file committed first (RED), then implementation (GREEN)
    - bson.MarshalExtJSON(doc, canonical=true, escapeHTML=false) for BSON type fidelity
    - bson.Unmarshal into typed struct for safe field extraction from bson.Raw

key-files:
  created:
    - internal/parser/mongodb/normalizer.go
    - internal/parser/mongodb/normalizer_test.go
  modified:
    - go.mod (added go.mongodb.org/mongo-driver/v2)
    - go.sum

key-decisions:
  - "mongo-driver v2 collapses primitive package into bson package — ObjectID, Timestamp, etc. are bson.ObjectID / bson.Timestamp (no bson/primitive sub-package)"
  - "replace operationType treated as update (full-document replacement is semantically an update)"
  - "BSON extended JSON canonical=true preserves $oid, $date, $numberDecimal wrappers for type fidelity"
  - "IdempotencyKey: sourceID:db.coll:keyJSON:op:clusterTimeHex — matches Postgres connector pattern"
  - "Resume token serialized via bson.Raw.String() — returns canonical extended JSON string of the _id document"

patterns-established:
  - "BSON serialization pattern: unmarshal into typed struct → marshal fields via bson.MarshalExtJSON"
  - "Nil json.RawMessage for absent before/after — consistent with event package convention"

requirements-completed: [SRC-10, PAR-04]

# Metrics
duration: 2min
completed: 2026-03-17
---

# Phase 9 Plan 02: MongoDB BSON Normalizer Summary

**BSON Change Stream normalizer mapping insert/update/delete/replace to unified ChangeEvent with extended JSON for ObjectID/$date/$decimal type fidelity**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-17T00:49:20Z
- **Completed:** 2026-03-17T00:51:50Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 4

## Accomplishments

- `NormalizeChangeEvent` converts raw `bson.Raw` Change Stream documents to `*event.ChangeEvent`
- All four operation types handled: insert, update, delete, replace (replace → OpUpdate)
- Extended JSON serialization (canonical mode) preserves BSON type wrappers (`$oid`, `$date`, `$numberDecimal`)
- Metadata populated with resume_token, snapshot=false, db, collection
- IdempotencyKey format matches Postgres connector: `sourceID:db.coll:keyJSON:op:clusterTimeHex`
- Added `go.mongodb.org/mongo-driver/v2` to module

## Task Commits

TDD task with RED → GREEN commits:

1. **RED — Failing tests** - `87bbee5` (test)
2. **GREEN — NormalizeChangeEvent implementation** - `73ba9f4` (feat)

**Plan metadata:** (docs commit)

_TDD task: test committed first, implementation second._

## Files Created/Modified

- `internal/parser/mongodb/normalizer.go` — NormalizeChangeEvent function with BSON decoding, op mapping, extended JSON serialization
- `internal/parser/mongodb/normalizer_test.go` — 9 test cases covering all op types, error paths, metadata, and idempotency key
- `go.mod` — added go.mongodb.org/mongo-driver/v2 v2.5.0
- `go.sum` — updated checksums

## Decisions Made

- **mongo-driver v2 package structure:** In v2, `primitive` types (`ObjectID`, `Timestamp`) moved into the top-level `bson` package — there is no `bson/primitive` sub-package. Tests updated to use `bson.ObjectID` and `bson.Timestamp` directly.
- **replace → OpUpdate:** MongoDB `replace` is a full-document swap, semantically equivalent to `update` in the unified event schema.
- **bson.MarshalExtJSON canonical=true:** Preserves BSON type wrappers needed for round-trip fidelity ($oid, $date, etc.) versus relaxed mode which loses type information.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Updated test imports for mongo-driver v2 package structure**
- **Found during:** Task 1 RED phase
- **Issue:** Plan specified `go.mongodb.org/mongo-driver/v2/bson/primitive` for `primitive.ObjectID` and `primitive.Timestamp`, but mongo-driver v2 merged these into the top-level `bson` package — `bson/primitive` does not exist in v2.
- **Fix:** Test file uses `bson.ObjectID`, `bson.NewObjectID()`, `bson.Timestamp` directly from `go.mongodb.org/mongo-driver/v2/bson`. No import of a non-existent sub-package.
- **Files modified:** `internal/parser/mongodb/normalizer_test.go`
- **Verification:** `go test ./internal/parser/mongodb/... -v` all 9 tests pass
- **Committed in:** `87bbee5` (RED commit)

---

**Total deviations:** 1 auto-fixed (blocking — package not found)
**Impact on plan:** Required import correction for v2 API; no semantic changes to the normalizer contract.

## Issues Encountered

- mongo-driver v2 reorganized `primitive` package into `bson` — discovered during initial `go test` run. Fixed immediately by updating import paths in test.

## User Setup Required

None — no external service configuration required.

## Self-Check: PASSED

All created files found. Both task commits verified.

## Next Phase Readiness

- `NormalizeChangeEvent` is ready for wiring into the MongoDB Change Stream consumer (Phase 09-03).
- Extended JSON encoding verified for ObjectID in both `key` and `after` fields.
- `go build ./...` passes with no errors.
