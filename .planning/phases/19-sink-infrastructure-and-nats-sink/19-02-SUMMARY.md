---
phase: 19
plan: 02
subsystem: output, nats
tags: [nats, jetstream, sink, consumer, tdd, router, metrics, prometheus]
dependency_graph:
  requires: [SinksConfig, NATSSinkConfig, QueuePublishTotal, QueuePublishErrors, QueuePublishLatency]
  provides: [NATSSinkConsumer, NewNATSSinkConsumer]
  affects: [internal/output/nats/consumer.go, internal/output/nats/consumer_test.go]
tech_stack:
  added: []
  patterns: [TDD red-green, router.Consumer interface, synchronous JetStream PubAck, go template subject routing, embedded NATS server in tests]
key_files:
  created:
    - internal/output/nats/consumer.go
    - internal/output/nats/consumer_test.go
  modified:
    - go.mod
    - go.sum
decisions:
  - "isInvalidNATSSubject validates subjects using the same logic as nats.go client's unexported badSubject() — nats.go v1.51.0 does not export a subject validation function"
  - "nc.Flush() added in TestNATSSinkConsumer_Deliver_SubjectTemplate after nc.Subscribe() to ensure server-side interest registration before publish — without it the test was flaky when the previous test left ephemeral goroutines"
  - "natsgo.Timeout(5*time.Second) added to connection options so NewNATSSinkConsumer fails fast on unreachable URLs rather than hanging"
metrics:
  duration_seconds: 264
  completed_date: "2026-05-04"
  tasks_completed: 1
  files_changed: 4
---

# Phase 19 Plan 02: NATSSinkConsumer Implementation Summary

**One-liner:** NATSSinkConsumer implements router.Consumer using synchronous JetStream PublishMsg with idempotency header, subject Go-template routing, and fail-fast stream validation.

## What Was Built

`internal/output/nats/consumer.go` — NATSSinkConsumer, a fully tested router.Consumer implementation that publishes CDC events to an external NATS JetStream server:

1. **NATSSinkConsumer struct** — holds id, `*nats.Conn`, `jetstream.JetStream`, parsed `*template.Template` for subject derivation, and optional `*observability.KaptantoMetrics`.

2. **NewNATSSinkConsumer(id, cfg)** — parses SubjectTemplate early (fail-fast on bad template), builds `nats.Conn` with reconnect options (`MaxReconnects(-1)`, `ReconnectWait(2s)`, `ReconnectJitter`), validates StreamName via `js.Stream()` with 5-second timeout if set.

3. **Deliver(ctx, entry)** — executes Go template against entry.Event to derive subject, validates subject with `isInvalidNATSSubject`, marshals event to JSON, sets `Kaptanto-Idempotency-Key` header, calls `js.PublishMsg` synchronously (blocks until PubAck, preserving CHK-01), records latency and increments Total/Errors counters.

4. **Ping()** — returns nil when `nc.IsConnected()` is true; error with status otherwise (OBS-02 groundwork).

5. **Close()** — calls `nc.Close()`; safe to call multiple times.

6. **SetMetrics(m)** — injects KaptantoMetrics after construction.

7. **Compile-time assertion** — `var _ router.Consumer = (*NATSSinkConsumer)(nil)`.

### Test Coverage (7 tests)

- `TestNATSSinkConsumer_Deliver_Success` — publish success, QueuePublishTotal incremented to 1
- `TestNATSSinkConsumer_Deliver_Header` — Kaptanto-Idempotency-Key header present with correct value
- `TestNATSSinkConsumer_Deliver_SubjectTemplate` — subject "cdc.orders" derived from template "cdc.{{.Table}}"
- `TestNATSSinkConsumer_Ping` — nil when connected, error after Close
- `TestNATSSinkConsumer_StreamValidation` — error containing stream name when stream not found
- `TestNATSSinkConsumer_ID` — ID() returns constructor argument
- `TestNATSSinkConsumer_InvalidURL` — error on unreachable URL

All tests use an in-process NATS server via `natstest.RunServer` (same pattern as eventlog tests).

## Verification Results

- `CGO_ENABLED=0 go build ./...` — PASS
- `CGO_ENABLED=0 go test ./internal/output/nats/... -v` — 7/7 PASS
- `CGO_ENABLED=0 go test ./...` — All 22 packages PASS (no regressions)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] natsgo.IsValidSubject does not exist in nats.go v1.51.0**
- **Found during:** Task 1 (GREEN phase, first compile attempt)
- **Issue:** The plan specified `natsgo.IsValidSubject(subject)` but this function is not exported in `github.com/nats-io/nats.go`. The internal function is `badSubject()` (unexported).
- **Fix:** Implemented `isInvalidNATSSubject(subject string) bool` in consumer.go mirroring the exact logic of `badSubject()`: checks for whitespace, empty string, and empty dot-separated tokens.
- **Files modified:** `internal/output/nats/consumer.go`

**2. [Rule 1 - Bug] TestNATSSinkConsumer_Deliver_SubjectTemplate flaky without nc.Flush()**
- **Found during:** Task 1 (GREEN phase, test execution)
- **Issue:** `nc.Subscribe("cdc.orders")` returned before the server had processed the subscription interest. When tests ran sequentially, the publish in `Deliver` could race ahead of subscription registration, causing a 3-second timeout.
- **Fix:** Added `require.NoError(t, nc.Flush())` after the subscribe call to synchronously confirm the subscription is registered server-side before publishing.
- **Files modified:** `internal/output/nats/consumer_test.go`

## Self-Check

### Files Created

- internal/output/nats/consumer.go — FOUND
- internal/output/nats/consumer_test.go — FOUND

### Commits

- ce629cb: test(19-02): add failing tests for NATSSinkConsumer
- da75751: feat(19-02): implement NATSSinkConsumer with NATS JetStream publish

## Self-Check: PASSED
