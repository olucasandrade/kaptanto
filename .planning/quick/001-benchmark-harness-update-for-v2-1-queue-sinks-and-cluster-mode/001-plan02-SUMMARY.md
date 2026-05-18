---
phase: quick-001
plan: 02
subsystem: bench
tags: [bench, nats, collector, docker-compose, adapter]
dependency_graph:
  requires: []
  provides: [RunKaptantoNATS, kaptanto-nats-data, kaptanto-nats-service, nats-service]
  affects: [bench/cmd/collector/main.go, bench/docker-compose.yml]
tech_stack:
  added: [github.com/nats-io/nats.go v1.52.0]
  patterns: [core-NATS-subscribe, reconnect-loop, non-blocking-channel-send]
key_files:
  created:
    - bench/internal/collector/adapters/kaptanto_nats.go
    - bench/config/kaptanto-nats.yaml
  modified:
    - bench/cmd/collector/main.go
    - bench/docker-compose.yml
    - bench/go.mod
    - bench/go.sum
decisions:
  - "Use core NATS subscribe (nc.Subscribe) rather than JetStream — no stream pre-creation needed in bench harness, kaptanto configured without stream-name publishes to core NATS subjects"
  - "Non-blocking channel send (select/default) in NATS message handler — prevents callback from stalling the subscription under backpressure"
  - "Reconnect loop with 200ms delay mirrors pattern from kaptanto.go and kaptanto_kafka.go for consistency"
metrics:
  duration: 4m
  completed: "2026-05-18T23:11:13Z"
  tasks_completed: 2
  files_changed: 6
---

# Phase quick-001 Plan 02: Kaptanto NATS Sink Adapter Summary

Core NATS subscribe adapter for kaptanto NATS sink with nats:2.10-alpine server and kaptanto-nats compose service on port 7658.

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 | Add nats.go dep and create kaptanto_nats.go adapter | d7b8c53 | bench/internal/collector/adapters/kaptanto_nats.go, bench/go.mod, bench/go.sum |
| 2 | Wire NATS adapter into collector main + add compose services and config | d7b8c53 | bench/cmd/collector/main.go, bench/docker-compose.yml, bench/config/kaptanto-nats.yaml |

## What Was Built

**kaptanto_nats.go** subscribes to a core NATS subject using `nc.Subscribe`. The function signature is `RunKaptantoNATS(ctx, natsURL, subject string, scenario *atomic.Value, out chan<- EventRecord)`. It uses a reconnect loop (200ms delay on error), calls `ExtractBenchTS` from peerdb.go to parse `_bench_ts` from the kaptanto ChangeEvent JSON in the NATS message body, and emits records tagged `tool=kaptanto-nats`.

**bench/config/kaptanto-nats.yaml** configures kaptanto with `output: nats`, `port: 7658`, `source-id: bench_nats`, and `sinks.nats.url: nats://nats:4222` with `subject-template: "cdc.{{.Schema}}.{{.Table}}"` (renders to `cdc.public.bench_events`, matching the `--kaptanto-nats-subject` flag default).

**docker-compose.yml** gains two new services:
- `nats` (nats:2.10-alpine) on ports 4222/8222, healthcheck via `wget http://localhost:8222/healthz`
- `kaptanto-nats` on port 7658, depends on postgres + nats, healthcheck via `wget http://localhost:7658/healthz`

**bench/cmd/collector/main.go** gains `--kaptanto-nats-url` and `--kaptanto-nats-subject` flags and launches `go adapters.RunKaptantoNATS(...)` after the existing goroutines.

## Decisions Made

1. **Core NATS over JetStream**: The plan's action section explicitly documents this choice — kaptanto's NATS sink without `stream-name` configured publishes to core NATS subjects, not a JetStream stream. Using `nc.Subscribe` avoids stream pre-creation complexity in the benchmark harness.

2. **Non-blocking channel send**: The NATS message handler uses `select { case out <- rec: default: }` so a full `adapterCh` never stalls the NATS subscription callback goroutine.

3. **Atomic commit**: Both tasks committed together as `d7b8c53` since the adapter, config, and compose service form one coherent unit.

## Deviations from Plan

None — plan executed exactly as written. The plan itself documented the JetStream→core NATS pivot inline (the action section ends with "Revise to use core NATS subscribe"), so the final implementation follows the revised specification.

## Verification Results

All checks passed:

```
go build ./...                          OK
grep RunKaptantoNATS kaptanto_nats.go   OK
grep nats-io/nats.go go.mod             OK
grep kaptanto-nats-url main.go          OK
grep image: nats:2.10-alpine compose    OK
grep kaptanto-nats: compose             OK
grep subject-template kaptanto-nats.yaml OK
docker compose config --quiet           OK (YAML valid)
```

## Self-Check: PASSED

- `/Users/lucasandrade/kaptanto/bench/internal/collector/adapters/kaptanto_nats.go` — FOUND
- `/Users/lucasandrade/kaptanto/bench/config/kaptanto-nats.yaml` — FOUND
- Commit `d7b8c53` — FOUND
