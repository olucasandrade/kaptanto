# Fix Plan: Malformed row JSON must not match the filter

## Problem

`internal/output/row_filter.go` `RowFilter.Match` currently returns `true` when either `Before` or `After` fails to unmarshal. This bypasses the configured `where` clause and can route a malformed event to an action that should not receive it. Simply returning `false` would silently ack/drop the event, violating at-least-once semantics for poison events.

## Proposed Approach

1. Change `RowFilter.Match` to return `(bool, error)` so callers can distinguish a successful non-match from a JSON parse failure.
2. Propagate the parse error through the delivery path as a `*transform.RuntimeError` or similar poison-classified error so the router can block the message group and, when configured, dead-letter the event.
3. Audit every caller of `Match` (action `matchConsumer`, router dispatch, tests) and update them to handle the error.
4. Add a regression test that feeds a malformed `After`/`Before` JSON event into the router and asserts it is poison-handled rather than delivered.

## Dependencies

- `internal/output/row_filter.go`: signature change and error wrapping.
- `internal/action/action.go` (`matchConsumer.Deliver`): handle `Match` errors.
- `internal/router/router.go`: ensure delivery errors are classified as poison where appropriate.
- `internal/output/row_filter_test.go` and router/action tests: update expectations and add coverage.

## Risks

- Signature change ripples through multiple packages and may break existing tests that expect `Match` to be silent.
- Error classification must be careful: a parse failure is deterministic/non-retryable (poison), not transient.
- Blast radius spans filtering, routing, and action dispatch.

## Test Plan

1. Unit tests in `internal/output` for `Match` returning error on malformed JSON and `false` on valid non-match.
2. Router-level test verifying a malformed-row event is blocked and its cursor advances only after DLQ/poison handling.
3. Action integration test confirming the event is not delivered to the webhook sink.

## Status

Implementation deferred until approved because this is a multi-package behavioral change that affects delivery semantics and poison handling.
