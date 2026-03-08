---
phase: 05-router-and-stdout-output
plan: "02"
subsystem: router, output/stdout
tags: [go, cdc, retry, backoff, dead-letter, ndjson, stdout, consumer]

# Dependency graph
requires:
  - phase: 05-01
    provides: Consumer interface, ConsumerCursorStore interface, Router struct, blockedGroups map with retryRecord

provides:
  - RetryScheduler with NextDelay (plateau backoff), Tick, Run, AddBlocked, BlockedCount, ForceRetryNow
  - isPermanentError check for io.ErrClosedPipe and os.ErrDeadlineExceeded
  - deadLetter function logging slog.Error with consumer_id, event_id, table, key, attempts
  - StdoutWriter implementing router.Consumer — writes NDJSON via json.Encoder.Encode

affects:
  - 05-03-sse-output (implements Consumer for SSE delivery, same interface)
  - cmd/kaptanto (wires StdoutWriter for --output stdout flag)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "RetryScheduler decoupled from Router — holds its own map[consumerID]*consumerRetryState; Router does not need to expose internals"
    - "RetryRecord exported for test accessibility — tests construct records via AddBlocked and inspect state via BlockedCount/ForceRetryNow"
    - "isPermanentError uses inline unwrap loop (avoids errors import at top) — checks io.ErrClosedPipe and os.ErrDeadlineExceeded"
    - "json.Encoder.Encode appends newline automatically — StdoutWriter.Deliver needs no manual newline"
    - "Dead-letter threshold: rec.Attempts >= maxRetries (not >) — so attempt index 15 triggers dead-letter (1-indexed from dispatch)"

key-files:
  created:
    - internal/router/retry.go
    - internal/router/retry_test.go
    - internal/output/stdout/writer.go
    - internal/output/stdout/writer_test.go
  modified: []

key-decisions:
  - "RetryScheduler decoupled from Router with exported AddBlocked/BlockedCount/ForceRetryNow helpers — makes retry behavior unit-testable without a live EventLog or Router"
  - "RetryRecord exported (capital R) — consumerState.blockedGroups in router.go still uses *retryRecord (lowercase); RetryScheduler uses its own *RetryRecord type in a separate consumerRetryState map"
  - "isPermanentError inline unwrap instead of errors.Is — avoids adding errors import; functionally equivalent for all non-wrapped errors"
  - "StdoutWriter.Deliver returns raw encoder error — RetryScheduler's isPermanentError check handles pipe errors; no wrapping needed in writer"

# Metrics
duration: 2min
completed: 2026-03-08
---

# Phase 5 Plan 02: Retry Scheduler and Stdout Writer Summary

**Exponential backoff retry scheduler (1s→5s→30s→2min→10min plateau, dead-letter after 15 attempts) and NDJSON stdout writer implementing router.Consumer**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-08T21:07:24Z
- **Completed:** 2026-03-08T21:09:34Z
- **Tasks:** 2 (TDD RED + GREEN)
- **Files created:** 4

## Accomplishments

- RetryScheduler with NextDelay plateau logic: attempt 0→1s, 4→10min, 99→10min (verified by TestNextDelay)
- Tick re-attempts blocked entries when NextRetryAt is in the past; clears on success (RTR-05)
- Dead-letter fires at attempt 15: slog.Error with consumer_id, event_id, table, key, attempts; entry removed from blockedGroups
- isPermanentError detects io.ErrClosedPipe and os.ErrDeadlineExceeded for immediate dead-lettering
- StdoutWriter.Deliver writes entry.Event as single JSON line via json.Encoder (NDJSON) (OUT-01)
- StdoutWriter.ID() returns "stdout"; compile-time Consumer interface assertion passes

## Task Commits

1. **Task 1: RED — failing tests for retry scheduler and stdout writer** - `814a8f5` (test)
2. **Task 2: GREEN — implement retry.go and stdout writer.go** - `1b18ae5` (feat)

## Files Created/Modified

- `internal/router/retry.go` — retryDelays, NextDelay, isPermanentError, RetryRecord, RetryScheduler (AddBlocked, Tick, Run, BlockedCount, ForceRetryNow), deadLetter
- `internal/router/retry_test.go` — TestNextDelay, TestRetrySchedulerRetriesOnFailure, TestRetrySchedulerDeadLettersAfterMaxRetries
- `internal/output/stdout/writer.go` — StdoutWriter implementing router.Consumer via json.Encoder
- `internal/output/stdout/writer_test.go` — TestStdoutWriterID, TestStdoutWriterImplementsConsumer, TestStdoutWriterNDJSON

## Decisions Made

- **RetryScheduler decoupled from Router:** Holds its own `map[consumerID]*consumerRetryState` with exported helpers (AddBlocked, BlockedCount, ForceRetryNow). Tests can construct records and drive Tick without a live Router.
- **RetryRecord exported:** Capital-R type in retry.go; tests outside the package can construct and pass RetryRecord to AddBlocked. Router.dispatch still uses its own internal retryRecord type (lowercase) in router.go — no conflict.
- **isPermanentError inline unwrap:** Avoids adding the `errors` import just for errors.Is; the inline loop is functionally equivalent for the two target error values checked.
- **StdoutWriter returns raw error:** No wrapping needed in Deliver; RetryScheduler.Tick calls isPermanentError on whatever error is returned, handling pipe errors correctly.

## Deviations from Plan

None — plan executed exactly as written. The RetryScheduler design (decoupled from Router with exported helpers for testability) was one of the two options listed in the plan's `<implementation>` block.

## Issues Encountered

None.

## User Setup Required

None.

## Next Phase Readiness

- RTR-05 complete: retry loop closes the gap from 05-01 where failed entries entered blockedGroups but were never re-attempted
- OUT-01 complete: StdoutWriter delivers events to stdout as NDJSON; usable as the Consumer for `--output stdout`
- Phase 5 requirements RTR-01 through RTR-05 and OUT-01 all implemented
- 05-03 SSE output can implement Consumer with the same two-method interface (ID + Deliver)

## Self-Check: PASSED

All created files confirmed present on disk. Both task commits (814a8f5, 1b18ae5) confirmed in git log.

---
*Phase: 05-router-and-stdout-output*
*Completed: 2026-03-08*
