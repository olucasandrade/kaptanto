---
phase: 20-sqs-sink
plan: 01
subsystem: config
tags: [aws, sqs, config, yaml, go]

# Dependency graph
requires:
  - phase: 19-sink-infrastructure-and-nats-sink
    provides: SinksConfig, TLSConfig, NATSSinkConfig pointer-field pattern, sinks_test.go test structure
provides:
  - SQSSinkConfig struct with QueueURL, Region, AccessKeyID, SecretAccessKey, TLS fields
  - SinksConfig.SQS *SQSSinkConfig pointer field (nil when sinks.sqs absent from YAML)
  - aws-sdk-go-v2/service/sqs, aws-sdk-go-v2/config, aws-sdk-go-v2/credentials in go.mod
affects: [20-02-sqs-sink-consumer, 20-03-cli-wiring, 21-kafka, 22-pubsub, 23-rabbitmq]

# Tech tracking
tech-stack:
  added:
    - github.com/aws/aws-sdk-go-v2 v1.41.7
    - github.com/aws/aws-sdk-go-v2/config v1.32.17
    - github.com/aws/aws-sdk-go-v2/service/sqs v1.42.27
    - github.com/aws/aws-sdk-go-v2/credentials v1.19.16
  patterns:
    - Pointer field on SinksConfig for nil-absence detection (consistent with NATS, Phase 19)
    - aws-sdk-go-v2 pure Go modules — no CGO, static binary compatible

key-files:
  created:
    - (none)
  modified:
    - internal/config/config.go
    - internal/config/sinks_test.go
    - go.mod
    - go.sum

key-decisions:
  - "SQSSinkConfig uses pointer field (*SQSSinkConfig) on SinksConfig — consistent with Phase 19 NATS decision for nil-absence detection vs zero-value confusion"
  - "aws-sdk-go-v2 chosen over aws-sdk-go v1 (deprecated) — v2 is pure Go, no CGO, static binary safe"
  - "credentials module included now for credentials.NewStaticCredentialsProvider used when AccessKeyID+SecretAccessKey are set in Plan 02 consumer"

patterns-established:
  - "Sink config pointer-field pattern: *SinkConfig on SinksConfig ensures nil when sub-block absent in YAML"
  - "TDD with three tests per config type: RoundTrip (all fields), AbsentBlock (nil check), TLS (nested struct)"

requirements-completed: [SNK-01]

# Metrics
duration: 5min
completed: 2026-05-04
---

# Phase 20 Plan 01: SQS Sink Config and AWS SDK Summary

**SQSSinkConfig struct and *SQS pointer field added to SinksConfig, aws-sdk-go-v2 modules installed — pure Go, no CGO introduced**

## Performance

- **Duration:** 5 min
- **Started:** 2026-05-04T13:01:04Z
- **Completed:** 2026-05-04T13:06:15Z
- **Tasks:** 2
- **Files modified:** 4 (config.go, sinks_test.go, go.mod, go.sum)

## Accomplishments
- Added SQSSinkConfig struct with QueueURL, Region, AccessKeyID, SecretAccessKey, TLS fields
- Added SinksConfig.SQS *SQSSinkConfig pointer field — nil when sinks.sqs absent from YAML
- Installed aws-sdk-go-v2/config, aws-sdk-go-v2/service/sqs, aws-sdk-go-v2/credentials via go get
- CGO_ENABLED=0 go build ./... passes — no CGO introduced

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: Add failing SQS config tests** - `55eb82c` (test)
2. **Task 1 GREEN: SQSSinkConfig type + SQS field on SinksConfig** - `f32cce8` (feat)
3. **Task 2: Install aws-sdk-go-v2 modules** - `4569994` (chore)

_TDD task has two commits (test RED → feat GREEN)_

## Files Created/Modified
- `internal/config/config.go` - Added SQSSinkConfig struct and SQS *SQSSinkConfig field on SinksConfig
- `internal/config/sinks_test.go` - Added TestSinks_SQS_RoundTrip, TestSinks_SQS_AbsentBlock, TestSinks_SQS_TLS
- `go.mod` - Added aws-sdk-go-v2/config, service/sqs, credentials (and their transitive deps)
- `go.sum` - Updated checksums for aws-sdk-go-v2 module tree

## Decisions Made
- Pointer field (*SQSSinkConfig) consistent with Phase 19 NATSSinkConfig pattern — nil when sub-block absent in YAML, avoids zero-value-struct ambiguity
- aws-sdk-go-v2 selected (v1 is deprecated); all v2 packages are pure Go — static binary remains CGO-free
- credentials module pulled in alongside config and service/sqs for use in Plan 02 (static credential provider when AccessKeyID + SecretAccessKey are set)

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Plan 02 (SQSSinkConsumer) can now import config.SQSSinkConfig and aws-sdk-go-v2/service/sqs — all compile-time dependencies in place
- Plan 03 (root.go wiring) depends on Plan 02 consumer type — unblocked after Plan 02
- SQS high-throughput FIFO mode opt-in question deferred to Plan 02/03 design (noted as open blocker in STATE.md)

---
*Phase: 20-sqs-sink*
*Completed: 2026-05-04*
