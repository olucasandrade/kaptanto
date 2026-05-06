---
phase: 23-rabbitmq-sink
plan: 01
subsystem: config
tags: [rabbitmq, amqp091-go, config, yaml, tls, sinks]

# Dependency graph
requires:
  - phase: 22-google-pubsub-sink
    provides: PubSubSinkConfig pattern — pointer field, TLSConfig reuse, three-test suite structure
provides:
  - RabbitMQSinkConfig struct with URL, Exchange, RoutingKeyTemplate, TLS fields
  - RabbitMQ *RabbitMQSinkConfig pointer field on SinksConfig
  - amqp091-go v1.11.0 in go.mod (indirect)
  - Three passing config round-trip tests for RabbitMQ
affects: [23-02-PLAN.md, 23-03-PLAN.md]

# Tech tracking
tech-stack:
  added: [github.com/rabbitmq/amqp091-go v1.11.0 (indirect)]
  patterns: [pointer-field nil-when-absent config pattern, TLSConfig reuse, three-test suite per sink]

key-files:
  created: []
  modified:
    - internal/config/config.go
    - internal/config/sinks_test.go
    - go.mod
    - go.sum

key-decisions:
  - "amqp091-go kept as indirect dependency (no import yet); go mod tidy omitted to preserve entry until Plan 02 imports it — mirrors Phase 21/22 Plan 01 decision for franz-go and pubsub/v2"
  - "RabbitMQSinkConfig uses pointer field (*RabbitMQSinkConfig) on SinksConfig — nil when sub-block absent in YAML, consistent with NATS/SQS/Kafka/PubSub pattern"
  - "TLSConfig reused from existing type — no new TLS struct needed for RabbitMQ"

patterns-established:
  - "Sink config TDD pattern: RED (failing tests) → commit → GREEN (struct + dependency) → commit"
  - "All five sink configs now use pointer fields and TLSConfig — consistent pattern across all queue sinks"

requirements-completed: [SNK-02]

# Metrics
duration: 2min
completed: 2026-05-07
---

# Phase 23 Plan 01: RabbitMQ Sink — Config and Dependency Summary

**RabbitMQSinkConfig struct with URL/Exchange/RoutingKeyTemplate/TLS fields added to config.go and amqp091-go v1.11.0 installed as indirect dependency, with three passing round-trip tests**

## Performance

- **Duration:** 2 min
- **Started:** 2026-05-06T22:45:03Z
- **Completed:** 2026-05-06T22:47:18Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments

- Added RabbitMQSinkConfig struct immediately before SinksConfig with four fields: URL, Exchange, RoutingKeyTemplate, TLS
- Added RabbitMQ *RabbitMQSinkConfig pointer field to SinksConfig after PubSub, removed stale "Phase 23" comment
- Installed github.com/rabbitmq/amqp091-go v1.11.0 (kept indirect — mirrors Phase 21/22 Plan 01 pattern)
- Three TestSinks_RabbitMQ_* tests pass: round-trip, absent block (nil check), TLS sub-fields

## Task Commits

Each task was committed atomically:

1. **TDD RED — RabbitMQ failing tests** - `b2b557e` (test)
2. **GREEN — RabbitMQSinkConfig + amqp091-go install** - `7bb6e30` (feat)

_Note: TDD tasks have two commits (test → feat)_

**Plan metadata:** committed as part of final docs commit

## Files Created/Modified

- `internal/config/config.go` - Added RabbitMQSinkConfig struct and RabbitMQ field on SinksConfig; removed stale Phase 23 comment
- `internal/config/sinks_test.go` - Added three TestSinks_RabbitMQ_* test functions
- `go.mod` - Added github.com/rabbitmq/amqp091-go v1.11.0 (indirect)
- `go.sum` - Updated checksums for amqp091-go

## Decisions Made

- amqp091-go kept as indirect dependency until Plan 02 imports it — consistent with Phase 21 (franz-go) and Phase 22 (pubsub/v2) approach; go mod tidy intentionally omitted to preserve entry
- Pointer field pattern (*RabbitMQSinkConfig) used — nil when YAML block absent — consistent across all five queue sinks

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

`make build CGO_ENABLED=0` failed with "no space left on device" — pre-existing system disk space issue (100% capacity on /dev/disk3s5), unrelated to this plan's changes. Code correctness confirmed via `go test ./internal/config/...` which compiles and runs all tests successfully (all pass).

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- RabbitMQSinkConfig is complete and ready for Plan 02 to import amqp091-go directly and implement the RabbitMQSinkConsumer
- amqp091-go v1.11.0 is in go.mod; Plan 02 should run go mod tidy after first import (same pattern as Phase 22 Plan 02 for pubsub/v2)

---
*Phase: 23-rabbitmq-sink*
*Completed: 2026-05-07*
