---
phase: 22-google-pubsub-sink
plan: 02
subsystem: output/pubsub
tags: [google-pubsub, pubsub/v2, pstest, router-consumer, tdd, sinks]

# Dependency graph
requires:
  - phase: 22-01
    provides: PubSubSinkConfig struct and cloud.google.com/go/pubsub/v2 in go.mod
  - phase: 21-kafka-sink
    provides: KafkaSinkConsumer pattern used as structural template
provides:
  - PubSubSinkConsumer in internal/output/pubsub/consumer.go
  - pstest-based unit tests in internal/output/pubsub/consumer_test.go
affects: [22-03]

# Tech tracking
tech-stack:
  added: ["cloud.google.com/go/pubsub/v2/pstest (test)", "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"]
  patterns:
    - "result.Get(ctx) blocks until broker ack — CHK-01 synchronous publish confirmation"
    - "publisher.EnableMessageOrdering = true set before first Publish — required for OrderingKey"
    - "errors.As(err, &paused) for ErrPublishingPaused detection — NOT string matching"
    - "grpc.NewClient used instead of deprecated grpc.Dial"
    - "option.WithGRPCConn injection pattern for pstest test connectivity"

key-files:
  created:
    - internal/output/pubsub/consumer.go
    - internal/output/pubsub/consumer_test.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "grpc.NewClient used (not deprecated grpc.Dial) — pstest fake_test.go uses NewClient; examples_test.go uses Dial but the non-deprecated form is preferred"
  - "NewPubSubSinkConsumer accepts variadic option.ClientOption as final parameter — enables pstest injection without a separate constructor or interface; production code passes no extra options"
  - "TopicTemplate preserved in config but not applied per-message in v2.6.0 — Publisher is fixed to topicID at construction (v2 API constraint); documented in code comment"
  - "go mod tidy run after import — pubsub/v2 was indirect in Plan 01; importing pubsub/v2 and pstest in Plan 02 promotes it to direct and resolves transitive deps"

patterns-established:
  - "pubsubsink package name follows natssink/sqssink/kafkasink convention"
  - "compile-time assertion: var _ router.Consumer = (*PubSubSinkConsumer)(nil)"
  - "startFakeServer + createTopic + makeConsumerWithFakeServer helpers mirror kafkasink test structure"

requirements-completed: [SNK-04]

# Metrics
duration: 8min
completed: 2026-05-06
---

# Phase 22 Plan 02: PubSubSinkConsumer Implementation Summary

**PubSubSinkConsumer with result.Get synchronous ack (CHK-01), OrderingKey routing (DLV-02), Kaptanto-Idempotency-Key attribute (DLV-04), ResumePublish recovery, and 6 passing pstest unit tests**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-05-06T11:31:18Z
- **Completed:** 2026-05-06T11:39:00Z
- **Tasks:** 1 (TDD)
- **Files modified:** 4

## Accomplishments

- Implemented `PubSubSinkConsumer` in `internal/output/pubsub/consumer.go`:
  - Implements `router.Consumer` (compile-time assertion at package level)
  - Constructor accepts variadic `option.ClientOption` for pstest injection
  - `publisher.EnableMessageOrdering = true` set before first Publish call
  - `Deliver` uses `result.Get(ctx)` to block until broker ack (CHK-01)
  - `OrderingKey = string(entry.Event.Key)` for per-key ordering (DLV-02)
  - `Kaptanto-Idempotency-Key` attribute on every message (DLV-04)
  - `errors.As(err, &paused)` detection with `publisher.ResumePublish` recovery
  - Metrics reported via `SetMetrics`: QueuePublishTotal, QueuePublishErrors, QueuePublishLatency
  - `Ping` via `TopicAdminClient.GetTopic` with 5-second timeout
  - `Close` calls `publisher.Stop()` before `client.Close()`
- Implemented 6 pstest-based unit tests in `internal/output/pubsub/consumer_test.go`:
  - `TestNewPubSubSinkConsumer_InvalidTemplate` — malformed template returns error
  - `TestPubSubSinkConsumer_Deliver_Success` — nil error + QueuePublishTotal == 1
  - `TestPubSubSinkConsumer_Deliver_OrderingKey` — OrderingKey and attribute verified via subscriber pull
  - `TestPubSubSinkConsumer_Ping` — nil on existing topic
  - `TestPubSubSinkConsumer_Close` — no panic, idempotent second call
  - `TestPubSubSinkConsumer_Deliver_MetricsError` — error + QueuePublishErrors >= 1
- All 6 tests pass with `CGO_ENABLED=0 go test ./internal/output/pubsub/... -v -count=1 -timeout 30s`
- Full project build clean: `CGO_ENABLED=0 go build ./...`

## Task Commits

Each task was committed atomically:

1. **Task 1: Implement PubSubSinkConsumer + pstest tests (TDD)** - `4e0e6d4` (feat)

## Files Created/Modified

- `internal/output/pubsub/consumer.go` — PubSubSinkConsumer implementation
- `internal/output/pubsub/consumer_test.go` — 6 pstest-based unit tests
- `go.mod` — pubsub/v2 promoted to direct; pstest/pubsubpb transitive deps resolved
- `go.sum` — updated checksums

## Decisions Made

- `grpc.NewClient` used instead of deprecated `grpc.Dial` — pstest's own `fake_test.go` uses `NewClient`; cleaner for the codebase
- `NewPubSubSinkConsumer` accepts variadic `option.ClientOption` — enables pstest injection without a separate internal constructor or interface indirection; production wiring (Plan 03) passes no extra options
- `TopicTemplate` preserved in config but not applied per-message — Pub/Sub Publisher is created for a fixed topicID at construction (v2 API design); documented in `Deliver` godoc; multi-topic support deferred to future version
- `go mod tidy` run in this plan — pubsub/v2 was intentionally kept indirect in Plan 01 to preserve the go.mod entry; now that Plan 02 imports it directly, tidy is appropriate

## Deviations from Plan

None — plan executed exactly as written. The plan specified `NewPubSubSinkConsumer(id string, cfg config.PubSubSinkConfig, clientOpts ...option.ClientOption)` signature implicitly via the test helper description; implemented as specified.

## Issues Encountered

None — all tests passed on first run after implementation.

## User Setup Required

None — pstest-based tests require no external services or GCP credentials.

## Next Phase Readiness

- Plan 03 (root.go wiring) can now import `pubsubsink "github.com/olucasandrade/kaptanto/internal/output/pubsub"` and call `pubsubsink.NewPubSubSinkConsumer(id, *cfg.Sinks.PubSub)` — no extra clientOpts needed in production

---
*Phase: 22-google-pubsub-sink*
*Completed: 2026-05-06*
