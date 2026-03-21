---
phase: 12-metrics-collector-and-scenarios
plan: "03"
subsystem: bench/scenarios
tags: [scenarios, orchestrator, crash-recovery, tdd, benchmark]
dependency_graph:
  requires: [12-01, 12-02]
  provides: [bench/cmd/scenarios binary, bench/internal/scenarios/runner.go]
  affects: [bench/cmd/collector (subprocess), bench/cmd/loadgen (subprocess)]
tech_stack:
  added: []
  patterns:
    - exec.Command subprocess orchestration with signal propagation
    - httptest.Server for testing HTTP-dependent helpers without real servers
    - TDD red-green cycle for scenario runner core
key_files:
  created:
    - bench/internal/scenarios/runner.go
    - bench/internal/scenarios/runner_test.go
    - bench/cmd/scenarios/main.go
  modified: []
decisions:
  - "ScenarioDef.PreWaitS=30 for steady (not baked into duration) — keeps SCN-01 warmup configurable without changing loadgen flags"
  - "buildLoadgenCmd always prepends --dsn to args — loadgen requires DSN; prepend avoids caller error"
  - "pollRecovery returns elapsed regardless of timeout — avoids -1 sentinel; caller logs the value"
  - "appendJSONLine opens file per call with O_APPEND — safe for low-frequency marker writes; no file handle leak"
metrics:
  duration: 179s
  tasks_completed: 2
  files_created: 3
  files_modified: 0
  completed_date: "2026-03-21"
---

# Phase 12 Plan 03: Benchmark Scenario Orchestrator Summary

**One-liner:** Scenario orchestrator sequencing all five CDC benchmark scenarios with per-scenario collector tagging, crash+recovery mechanics, and PeerDB/Sequin initialization.

## What Was Built

### bench/internal/scenarios/runner.go

The `Runner` struct coordinates the full benchmark lifecycle:

- **`Scenarios []ScenarioDef`** — 5 canonical entries: steady (--rate 10000, 30s pre-wait), burst, large-batch, crash-recovery (120s steady loadgen), idle
- **`Runner.Init`** — Creates PeerDB Kafka peer and mirror via `psql -p 9900`; registers Sequin push consumer at `http://collector:8082/ingest/sequin` via curl to `http://localhost:7376/api/http_push_consumers`
- **`Runner.Run`** — Sequences scenarios: set tag via `POST /scenario?name=X`, write start marker to metrics.jsonl, optional PreWaitS warmup, run loadgen or crash-recovery, write end marker, 5s drain window
- **`Runner.runCrashRecovery`** — Starts loadgen in background, waits 30s for steady state, then for each of `[kaptanto, debezium, sequin, peerdb]`: `docker kill --signal SIGKILL`, `docker start`, polls `GET /scenario/last-event?tool=X` every 500ms (120s timeout) until last_receive_ts advances past killTime, writes `{"scenario_event":"recovery","tool":"X","recovery_seconds":N}` to metrics.jsonl
- **`setScenarioTag`** — POST to collector management API `/scenario?name=X`; errors on non-200
- **`writeMarker`** — Appends `{"scenario_event":event,"scenario":name,"ts":...}` to outputDir/metrics.jsonl
- **`buildLoadgenCmd`** — Prepends `--dsn` then scenario LoadgenArgs; returns exec.Cmd

### bench/cmd/scenarios/main.go

CLI entry point with flags: `--dsn`, `--output-dir`, `--scenarios`, `--collector-bin`, `--loadgen-bin`, `--collector-url`, `--statsd-bin`.

Startup sequence:
1. Resolve DSN (flag → `BENCH_DSN` → default)
2. `os.MkdirAll(outputDir)`
3. Start collector subprocess, redirect stdout/stderr, poll `GET /scenario` up to 5s for readiness
4. Optionally start statsd subprocess
5. `runner.Init` then `runner.Run` with filtered or full scenario list
6. SIGTERM to collector on exit

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check

Files exist:
- bench/internal/scenarios/runner.go — FOUND
- bench/internal/scenarios/runner_test.go — FOUND
- bench/cmd/scenarios/main.go — FOUND

Commits:
- 6e554d1 — test(12-03): failing tests (RED)
- 67a8c67 — feat(12-03): scenario runner core (GREEN)
- 96d697e — feat(12-03): scenario orchestrator binary

All 6 tests pass. `go build ./...` exits 0. `go test ./... -race` passes all packages.

## Self-Check: PASSED
