---
phase: 22-google-pubsub-sink
plan: 03
subsystem: cmd
tags: [pubsub, wiring, cli, tests]
dependency_graph:
  requires: [22-01, 22-02]
  provides: [pubsub-cli-mode, pubsub-e2e-wiring]
  affects: [internal/cmd/root.go, internal/cmd/root_test.go]
tech_stack:
  added: []
  patterns: [sink-wiring-pattern, nil-config-guard, obs-server-on-cfg-port]
key_files:
  modified:
    - internal/cmd/root.go
    - internal/cmd/root_test.go
decisions:
  - "pubsubsink import alias mirrors natssink/sqssink/kafkasink convention — consistent naming pattern"
  - "defer pubsubSink.Close() required — PubSubSinkConsumer holds a gRPC connection pool that must be drained on shutdown"
  - "obs server listens on cfg.Port (not cfg.Port+1) — Pub/Sub publishes to external GCP endpoint; no TCP server binds cfg.Port in pubsub mode"
  - "default error message updated to include pubsub in valid modes list"
metrics:
  duration: "1 minute"
  completed_date: "2026-05-06"
  tasks_completed: 3
  files_modified: 2
---

# Phase 22 Plan 03: root.go PubSub Wiring Summary

Wire `case "pubsub":` into root.go output switch with pubsubsink import alias, nil-config guard, gRPC pool drain via defer Close(), and two cmd tests verifying nil-config error and valid-modes error message.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Add case "pubsub": to root.go output switch | 3055042 | internal/cmd/root.go |
| 2 | Add cmd tests for pubsub nil-config and invalid mode | 1ffbc72 | internal/cmd/root_test.go |
| 3 | Full build and test verification | (no files) | — |

## What Was Built

### root.go Changes

Added `pubsubsink` import alias for `internal/output/pubsub` after the `kafkasink` line. Added `case "pubsub":` block in the output switch after `case "kafka":` and before `default:`. The block follows the established sink wiring pattern exactly:

1. Nil-check `cfg.Sinks.PubSub` — returns error containing `sinks.pubsub` if missing
2. Construct `pubsubsink.NewPubSubSinkConsumer("pubsub", *pubsubCfg)`
3. `defer pubsubSink.Close()` — drains gRPC connection pool
4. `pubsubSink.SetMetrics(metrics)`
5. `rtr.Register(pubsubSink)`
6. Append `healthProbes` with `pubsubSink.Ping`
7. Wire obs HTTP server on `cfg.Port` serving `/metrics` and `/healthz`

Updated `default:` case error message to append `pubsub` to the valid modes list.

### root_test.go Changes

Added two tests after `TestOutputMode_Kafka_InvalidMode`, mirroring the Kafka test pair:

- `TestOutputMode_PubSub_MissingConfig` — verifies error contains `sinks.pubsub`; no GCP connection required
- `TestOutputMode_PubSub_InvalidMode` — verifies error contains `pubsub` in valid modes list

## Verification Results

- `CGO_ENABLED=0 go build ./...` — passes
- `CGO_ENABLED=0 go test ./internal/output/pubsub/... -count=1` — 6/6 tests pass
- `CGO_ENABLED=0 go test ./internal/cmd/... -count=1` — all tests pass (including 2 new PubSub tests)
- `make verify-no-cgo` — linux/amd64 and darwin/arm64 cross-compile both pass

## Decisions Made

1. **pubsubsink import alias**: Mirrors natssink/sqssink/kafkasink convention — consistent naming pattern for all sink packages.
2. **defer pubsubSink.Close() required**: PubSubSinkConsumer holds a gRPC connection pool that must be drained. Unlike stateless HTTP SQS sink, Pub/Sub maintains persistent gRPC connections.
3. **obs server on cfg.Port**: Pub/Sub publishes to external GCP endpoint; no TCP server binds cfg.Port in pubsub mode. Matches nats/sqs/kafka obs server pattern.

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check: PASSED

- `internal/cmd/root.go` contains `case "pubsub":` at line 659
- `internal/cmd/root.go` contains `pubsubsink` import alias at line 37
- `internal/cmd/root_test.go` contains `TestOutputMode_PubSub_MissingConfig` and `TestOutputMode_PubSub_InvalidMode`
- Commits 3055042 and 1ffbc72 exist in git log
- All builds and tests pass
