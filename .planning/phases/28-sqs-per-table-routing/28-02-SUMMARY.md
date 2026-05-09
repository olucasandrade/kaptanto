---
phase: 28-sqs-per-table-routing
plan: 02
subsystem: sinks
tags: [sqs, aws, fifo, template, routing, tests, tdd, go-templates]

# Dependency graph
requires:
  - phase: 28-sqs-per-table-routing
    plan: 01
    provides: QueueURLTemplate config field, resolveQueueURL, getOrValidateQueue, updated Deliver
provides:
  - 3 YAML round-trip tests for QueueURLTemplate in SQSSinkConfig
  - 5 routing/pool/error/regression tests for SQSSinkConsumer
  - getQueueAttributesCallCount on fakeSQSClient for pool caching assertions
  - newTemplateConsumer helper for direct template injection in tests
affects:
  - internal/config/sinks_test.go
  - internal/output/sqs/consumer_test.go

# Tech tracking
tech-stack:
  added: [text/template (test import)]
  patterns:
    - getQueueAttributesCallCount on fakeSQSClient for lazy validation pool assertions
    - newTemplateConsumer helper — direct struct construction with parsed template, no AWS I/O

key-files:
  created: []
  modified:
    - internal/config/sinks_test.go
    - internal/output/sqs/consumer_test.go

key-decisions:
  - "getQueueAttributesCallCount added to fakeSQSClient and incremented before nil-func check — consistent with existing pattern, counts all calls including those from getQueueAttributesFunc"
  - "newTemplateConsumer seeds validatedQueues with defaultURL so pre-existing fake routing tests are not contaminated by unexpected GetQueueAttributes calls for the fallback URL"
  - "{{if false}}something{{end}} template used for empty-string guard test — same pattern established in Phase 25 Plan 02 for PubSub"
  - "TestSQSSinkConsumer_Routing_TemplateParseError tests template.Parse directly rather than via NewSQSSinkConsumer — avoids need for live AWS and still verifies the fail-fast parse behavior"

# Metrics
duration: 2min
completed: 2026-05-09
---

# Phase 28 Plan 02: SQS Per-Table Routing Tests Summary

**3 YAML config round-trip tests and 5 routing/caching/error consumer tests covering the QueueURLTemplate implementation from Plan 01**

## Performance

- **Duration:** ~2 min
- **Started:** 2026-05-09T16:51:40Z
- **Completed:** 2026-05-09T16:53:47Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments

- Added 3 YAML round-trip tests for `QueueURLTemplate` in `SQSSinkConfig`: full block with both fields, template-only (QueueURL empty), absent template (regression)
- Added `getQueueAttributesCallCount` to `fakeSQSClient` to enable pool caching assertions
- Added `newTemplateConsumer` test helper for constructing consumers with a parsed template without AWS I/O
- Added 5 new routing/pool/error/regression tests covering all scenarios from the plan
- All 16 pre-existing SQS consumer tests continue to pass (zero regressions)
- `CGO_ENABLED=0 go build ./...` clean; all tests green

## Task Commits

Each task was committed atomically:

1. **Task 1: Add SQS QueueURLTemplate YAML round-trip tests** - `1ac997f` (test)
2. **Task 2: Add routing, pool caching, and error tests to consumer_test.go** - `49f4e6f` (test)

## Files Created/Modified

- `/Users/lucasandrade/kaptanto/internal/config/sinks_test.go` — Appended 3 tests: TestSinks_SQS_QueueURLTemplate_FullBlock, TestSinks_SQS_QueueURLTemplate_TemplateOnly, TestSinks_SQS_QueueURLTemplate_AbsentTemplate
- `/Users/lucasandrade/kaptanto/internal/output/sqs/consumer_test.go` — Added `text/template` import; added `getQueueAttributesCallCount` field and updated `GetQueueAttributes` to increment it; added `newTemplateConsumer` helper; added 5 routing tests

## Decisions Made

- `getQueueAttributesCallCount` incremented unconditionally in `GetQueueAttributes` (before nil-func check) so pool caching tests measure all calls, consistent with how the real AWS SDK counts calls
- `newTemplateConsumer` seeds `validatedQueues` with the fallback default URL to prevent accidental GetQueueAttributes calls for the default URL during routing tests
- Template parse error test uses `template.Parse` directly — avoids requiring live AWS while still confirming the fail-fast parse behavior present in `NewSQSSinkConsumer`

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - all tests run offline with `fakeSQSClient`.

## Next Phase Readiness

- Phase 28 is complete (both plans done): CFG-02 for SQS is closed, implementation and tests verified
- Pattern established for per-table routing tests is consistent with Phase 25 PubSub approach

---
*Phase: 28-sqs-per-table-routing*
*Completed: 2026-05-09*
