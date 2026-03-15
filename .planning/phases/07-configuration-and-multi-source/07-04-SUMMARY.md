---
phase: 07-configuration-and-multi-source
plan: 04
subsystem: output
tags: [sse, grpc, row-filter, column-filter, cdc, cfg-05, cfg-06]

# Dependency graph
requires:
  - phase: 07-01
    provides: ApplyColumnFilter and output package (column_filter.go, row_filter.go)
  - phase: 07-02
    provides: RowFilter.Match and ParseRowFilter (row_filter.go)
provides:
  - SSEConsumer.Deliver calls RowFilter.Match (CFG-06) and ApplyColumnFilter (CFG-05)
  - GRPCConsumer.Deliver calls RowFilter.Match (CFG-06) and ApplyColumnFilter (CFG-05)
  - Both constructors accept rowFilter *output.RowFilter and allowedColumns []string
affects:
  - 08-integration
  - any phase that wires consumers to config-layer filters

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Shallow event copy pattern: copy *entry.Event to filtered struct, assign filtered Before/After — never mutate shared pointer"
    - "Pass-through by nil convention: nil rowFilter and nil/empty allowedColumns produce identical behavior to pre-filter consumers"
    - "Filter ordering in Deliver: EventFilter.Allow first, then RowFilter.Match, then ApplyColumnFilter — event rejected early avoids encoding work"

key-files:
  created:
    - internal/output/sse/consumer_test.go
    - internal/output/grpc/consumer_test.go
  modified:
    - internal/output/sse/consumer.go
    - internal/output/sse/server.go
    - internal/output/sse/server_test.go
    - internal/output/grpc/consumer.go
    - internal/output/grpc/server.go
    - internal/output/grpc/server_test.go

key-decisions:
  - "Shallow event copy (filtered := *ev) prevents mutation of shared event pointer across concurrent consumers in Router fan-out"
  - "nil rowFilter / nil allowedColumns treated as pass-through — backward-compatible with all existing call sites"
  - "Row filter placed before column filter in Deliver — filtered rows skip encoding work entirely"

patterns-established:
  - "Consumer filter wiring pattern: EventFilter.Allow -> RowFilter.Match -> ApplyColumnFilter(Before) -> ApplyColumnFilter(After) -> encode"
  - "New constructor params appended after existing params with nil defaults — keeps old call sites compilable with two added nil args"

requirements-completed: [CFG-05, CFG-06]

# Metrics
duration: 7min
completed: 2026-03-15
---

# Phase 7 Plan 4: Filter Wiring into SSE and gRPC Consumers Summary

**ApplyColumnFilter (CFG-05) and RowFilter.Match (CFG-06) wired into SSEConsumer.Deliver and GRPCConsumer.Deliver via shallow event copy pattern, closing the gap that made config-level `columns:` and `where:` settings unreachable at runtime**

## Performance

- **Duration:** 7 min
- **Started:** 2026-03-15T11:27:00Z
- **Completed:** 2026-03-15T11:34:30Z
- **Tasks:** 2 (both TDD)
- **Files modified:** 6

## Accomplishments
- SSEConsumer extended with `rowFilter` and `allowedColumns` fields; Deliver wires both filters before SSE wire writes
- GRPCConsumer extended identically; Deliver wires both filters before `json.Marshal` into proto payload
- Shallow copy pattern (`filtered := *ev`) prevents mutation of shared event pointer across Router fan-out consumers
- All existing SSE and gRPC tests continue to pass with nil pass-through (full backward compatibility)

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: SSEConsumer failing tests** - `f31b2ad` (test)
2. **Task 1 GREEN: Wire SSEConsumer filters** - `42e9dd2` (feat)
3. **Task 2 RED: GRPCConsumer failing tests** - `f71b3e8` (test)
4. **Task 2 GREEN: Wire GRPCConsumer filters** - `f850c24` (feat)

_Note: TDD tasks have two commits each (RED test → GREEN implementation)_

## Files Created/Modified
- `internal/output/sse/consumer.go` - Added rowFilter/allowedColumns fields, updated NewSSEConsumer signature, wired filters in Deliver
- `internal/output/sse/server.go` - Updated NewSSEConsumer call to pass nil, nil for new params
- `internal/output/sse/server_test.go` - Updated two NewSSEConsumer call sites to pass nil, nil
- `internal/output/sse/consumer_test.go` - New TDD test file: 5 tests for nil pass-through, row filter miss/hit, column filter strip, no-mutation guarantee
- `internal/output/grpc/consumer.go` - Added rowFilter/allowedColumns fields, updated NewGRPCConsumer signature, wired filters in Deliver
- `internal/output/grpc/server.go` - Updated NewGRPCConsumer call to pass nil, nil for new params
- `internal/output/grpc/server_test.go` - Updated four NewGRPCConsumer call sites to pass nil, nil
- `internal/output/grpc/consumer_test.go` - New TDD test file: 5 tests mirroring SSE test structure for gRPC channel semantics

## Decisions Made
- Shallow event copy (`filtered := *ev`) used consistently in both consumers to avoid mutating the shared `entry.Event` pointer that Router fan-out passes to all registered consumers
- nil rowFilter and nil/empty allowedColumns treated as pass-through — preserves full backward compatibility; existing call sites updated with `nil, nil` suffix
- Row filter check placed before column filter application in Deliver — filtered-out rows skip all encoding work

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- CFG-05 (column filtering) and CFG-06 (row filtering) are now fully wired end-to-end: config -> constructor -> Deliver -> wire output
- Phase 8 integration can wire `rowFilter` and `allowedColumns` from parsed config into consumer constructors
- Pre-existing EventFilter (table/operation filtering) remains unmodified and fully functional

---
*Phase: 07-configuration-and-multi-source*
*Completed: 2026-03-15*
