---
phase: 19
plan: 01
subsystem: config, observability
tags: [config, sinks, nats, prometheus, metrics, tdd]
dependency_graph:
  requires: []
  provides: [SinksConfig, NATSSinkConfig, TLSConfig, QueuePublishTotal, QueuePublishErrors, QueuePublishLatency]
  affects: [internal/config/config.go, internal/observability/metrics.go]
tech_stack:
  added: []
  patterns: [custom prometheus.Registry, YAML struct tags, pointer-optional sub-blocks]
key_files:
  created:
    - internal/config/sinks_test.go
  modified:
    - internal/config/config.go
    - internal/observability/metrics.go
    - internal/observability/metrics_test.go
decisions:
  - "NATS *NATSSinkConfig is a pointer field in SinksConfig so that absence of the sinks.nats YAML block results in nil, not a zero-value struct"
  - "No Merge() or Defaults() changes needed — sinks has no CLI flag in this phase (CFG-04 CLI flag deferred to Plan 03)"
  - "queue_publish_total metric name does not use kaptanto_ prefix, consistent with naming convention established in plan spec"
metrics:
  duration_seconds: 111
  completed_date: "2026-05-03"
  tasks_completed: 2
  files_changed: 4
---

# Phase 19 Plan 01: Sink Infrastructure and NATS Sink — Config and Metrics Foundation Summary

**One-liner:** YAML sinks config block with TLS/NATS types plus three Prometheus metric vectors for queue sink observability, all registered in the custom prometheus.Registry with no global state.

## What Was Built

Added the foundational shared types that all five sink phases (19-23) depend on:

1. **SinksConfig, NATSSinkConfig, TLSConfig in `internal/config/config.go`** — Pointer-optional `*NATSSinkConfig` field on `SinksConfig` ensures that a YAML file without a `sinks:` block results in `cfg.Sinks.NATS == nil`. TLSConfig provides ca-file, cert-file, key-file for mutual TLS and custom CA support.

2. **Three Prometheus metric vectors in `internal/observability/metrics.go`** — `QueuePublishTotal`, `QueuePublishErrors`, `QueuePublishLatency` all use a `{sink}` label for per-sink-type observability and are registered in the custom prometheus.Registry (no global DefaultRegisterer).

## Verification Results

- `go test ./internal/config/... -run TestSinks` — 4/4 PASS
- `go test ./internal/observability/... -run TestQueuePublishMetrics` — 8/8 PASS
- `CGO_ENABLED=0 go build ./...` — PASS (no new imports)
- `CGO_ENABLED=0 go test ./...` — All 22 packages PASS

## Deviations from Plan

None - plan executed exactly as written.

## Self-Check

### Files Created
- internal/config/sinks_test.go — FOUND
- internal/observability/metrics_test.go — modified (pre-existing)

### Files Modified
- internal/config/config.go — contains `SinksConfig` and `Sinks SinksConfig` field
- internal/observability/metrics.go — contains `QueuePublishTotal`, `QueuePublishErrors`, `QueuePublishLatency`

### Commits
- 3633362: feat(19-01): add SinksConfig, NATSSinkConfig, TLSConfig to config.go
- 0085389: feat(19-01): add queue publish metrics to KaptantoMetrics
