---
phase: 21-kafka-sink
plan: "01"
subsystem: config
tags: [kafka, franz-go, config, sink, yaml]

# Dependency graph
requires:
  - phase: 20-sqs-sink
    provides: SQSSinkConfig pattern — pointer field on SinksConfig, nil when absent
provides:
  - KafkaSinkConfig type in internal/config/config.go with all 6 fields
  - SinksConfig.Kafka *KafkaSinkConfig pointer field
  - franz-go v1.21.1 in go.mod (pure-Go, CGO-free Kafka client)
affects:
  - 21-02-kafka-sink-consumer
  - 21-03-kafka-cli-wiring

# Tech tracking
tech-stack:
  added: [github.com/twmb/franz-go v1.21.1]
  patterns: [pointer field on SinksConfig (nil when YAML block absent), TDD RED-GREEN for config structs]

key-files:
  created: []
  modified:
    - internal/config/config.go
    - internal/config/sinks_test.go
    - go.mod
    - go.sum

key-decisions:
  - "franz-go added as indirect dependency; import deferred to Plan 02 to avoid go mod tidy removal"
  - "KafkaSinkConfig uses pointer field (*KafkaSinkConfig) on SinksConfig — nil when absent, consistent with NATS and SQS pattern"
  - "No Merge()/Defaults() changes — Kafka sink has no CLI flag equivalents in this plan"

patterns-established:
  - "KafkaSinkConfig follows SQSSinkConfig pointer-field pattern exactly"
  - "TLSConfig reused from shared type — no per-sink TLS struct duplication"

requirements-completed: [SNK-03]

# Metrics
duration: 7min
completed: 2026-05-05
---

# Phase 21 Plan 01: Kafka Sink Config Summary

**KafkaSinkConfig struct with 6 fields added to config.go and franz-go v1.21.1 (CGO-free) added to go.mod**

## Performance

- **Duration:** 7 min
- **Started:** 2026-05-05T11:55:19Z
- **Completed:** 2026-05-05T12:02:00Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments

- KafkaSinkConfig struct (BootstrapServers, TopicTemplate, SASLMechanism, SASLUsername, SASLPassword, TLS) added to config.go
- SinksConfig.Kafka *KafkaSinkConfig pointer field added — nil when sinks.kafka absent in YAML
- franz-go v1.21.1 pinned in go.mod with no CGO requirement; CGO_ENABLED=0 make build verified clean
- 4 new TDD tests cover round-trip, absent block, no-SASL defaults, and no-sinks-block cases

## Task Commits

Each task was committed atomically:

1. **TDD RED: add failing tests for KafkaSinkConfig** - `11d2816` (test)
2. **Task 1: Add KafkaSinkConfig and update SinksConfig** - `f8bf548` (feat)
3. **Task 2: Install franz-go v1.21.1** - `09c84cd` (chore)

_Note: TDD task has separate test commit (RED) before implementation commit (GREEN)_

## Files Created/Modified

- `internal/config/config.go` - KafkaSinkConfig struct + Kafka *KafkaSinkConfig field on SinksConfig; updated doc comment
- `internal/config/sinks_test.go` - 4 new Kafka config tests (RoundTrip, AbsentBlock, NoSASL, NoSinksBlock)
- `go.mod` - franz-go v1.21.1 added as indirect dependency
- `go.sum` - updated checksums

## Decisions Made

- franz-go kept as `// indirect` in go.mod (no import yet — Plan 02 will import it). Running `go mod tidy` after `go get` without an import would remove the entry, so tidy was intentionally skipped for this plan.
- KafkaSinkConfig follows the exact SQSSinkConfig/NATSSinkConfig pointer-field pattern established in Phases 19-20.
- No CLI flag equivalents for Kafka sink fields in this plan (CFG-04 only covers --output switch, handled in Plan 03).

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

`go mod tidy` after `go get github.com/twmb/franz-go@v1.21.1` removed the entry because no .go file imports it yet. Resolved by re-running `go get` without a subsequent `go mod tidy` — the dependency is pinned as `// indirect` for Plan 02 to activate via import.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Plan 02 can import `github.com/twmb/franz-go/pkg/kgo` directly — dependency is in go.mod
- KafkaSinkConfig is ready for use in KafkaSinkConsumer constructor
- SinksConfig.Kafka nil-check pattern matches NATS and SQS for consistent CLI wiring in Plan 03

---
*Phase: 21-kafka-sink*
*Completed: 2026-05-05*
