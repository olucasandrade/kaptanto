---
phase: 06-sse-and-grpc-servers
status: passed
verified: 2026-03-15
requirements:
  satisfied:
    - OUT-02
    - OUT-03
    - OUT-04
    - OUT-05
    - OUT-06
    - OUT-07
    - OUT-08
    - CFG-03
    - CFG-04
    - OBS-01
    - OBS-02
  fixed_in_later_phase:
    - id: CHK-02
      fixed_in: "Phase 7.1 (07.1-01-PLAN.md)"
      defect: "LogEntry.PartitionID missing; SSEConsumer.Deliver passed hardcoded 0 to SaveCursor for all partitions"
---

# Phase 6: SSE and gRPC Servers Verification Report

**Phase Goal:** Browser-native SSE streaming and gRPC server-streaming with per-consumer cursor persistence, event filtering, Prometheus metrics, and health endpoint.
**Verified:** 2026-03-15
**Status:** passed

## Goal Achievement

Phase 6 delivered all planned output servers (SSE and gRPC), observability infrastructure, cursor persistence, and event filtering. One defect — CHK-02's `LogEntry.PartitionID` field being absent — caused SSEConsumer to pass a hardcoded `partitionID=0` to `SaveCursor` for all partitions. This was noted as a TODO in 06-03-SUMMARY.md and fixed in Phase 7.1 plan 07.1-01. All other requirements are fully satisfied.

## Requirements Coverage

### OUT-02: SSE server supports multiple independent consumer connections

**Status:** SATISFIED
**Evidence (06-03-SUMMARY.md):** `SSEServer` is an `http.Handler`; each incoming HTTP request creates an independent `SSEConsumer` with its own cursor state and Router registration. Test "Two concurrent consumers are independent (distinct IDs registered)" confirms that two simultaneous connections receive distinct `consumer.ID()` values and are tracked independently by the Router.

### OUT-03: SSE server supports Last-Event-ID header for automatic resume on reconnect

**Status:** SATISFIED
**Evidence (06-03-SUMMARY.md):** `SSEServer.ServeHTTP` reads the `Last-Event-ID` request header and passes the consumer ID to the Router via `router.Register`; the Router loads the persisted cursor from `SQLiteCursorStore` via `consumer.ID()`. Test "Cursor resume: SSEConsumer with same consumerID starts at persisted seq=42, not seq=1" passes.

### OUT-04: SSE server sends periodic ping comments to keep connections alive through proxies

**Status:** SATISFIED
**Evidence (06-03-SUMMARY.md):** A ping ticker fires every 15s (configurable) and writes `": ping\n\n"` to the response writer. Test "Ping keepalive `: ping` comment sent on interval" confirms the comment is written within the configured interval.

### OUT-05: SSE server supports configurable CORS origins

**Status:** SATISFIED
**Evidence (06-03-SUMMARY.md):** `SSEServer` sets `Access-Control-Allow-Origin` to the configured origin before writing the SSE content-type header. Test "Access-Control-Allow-Origin: configured-origin header" confirms the header is present on SSE responses.

### OUT-06: gRPC server implements Subscribe (server-streaming) and Acknowledge (unary) RPCs

**Status:** SATISFIED
**Evidence (06-04-SUMMARY.md):** `cdc.proto` defines `CdcStream` service with `Subscribe` (server-streaming) and `Acknowledge` (unary). `GRPCServer.Subscribe` creates a `GRPCConsumer`, registers it with the Router, and loops `stream.Send()` for each event. `GRPCServer.Acknowledge` calls `cursorStore.SaveCursor` with the client-acknowledged sequence number. Test "Subscribe loop delivers events from channel to stream" and "Acknowledge calls SaveCursor and returns ok=true" both pass.

### OUT-07: gRPC server supports protobuf serialization with JSON fallback

**Status:** SATISFIED
**Evidence (06-04-SUMMARY.md):** `ChangeEvent.payload` in `cdc.proto` carries the full JSON-encoded event bytes. `GRPCConsumer.Deliver` encodes via the JSON codec and places the result in the `payload` field, providing JSON fallback inside the protobuf envelope. Hand-written stubs are marked TODO for protoc regeneration when CI supports it.

### OUT-08: gRPC server uses HTTP/2 native backpressure for flow control

**Status:** SATISFIED
**Evidence (06-04-SUMMARY.md):** `GRPCServer.Subscribe` calls `stream.Send()` **outside** all Router locks via the channel-bridge pattern. `GRPCConsumer.Deliver` sends to a buffered channel (non-blocking) and returns an error when the channel is full, signalling backpressure to `RetryScheduler`. This prevents holding any Router lock during HTTP/2 flow-control pauses. Test "Channel full returns error (backpressure to RetryScheduler)" confirms the backpressure signal.

### CFG-03: Kaptanto supports table filtering (include specific tables)

**Status:** SATISFIED
**Evidence (06-01-SUMMARY.md):** `EventFilter` with `map[string]struct{}` table allow-list implemented in `internal/output/filter.go`. `nil` map means all-allowed (pass-through); populated map restricts to named tables. Both `SSEConsumer.Deliver` and `GRPCConsumer.Deliver` call `filter.Allow(event)` before writing to wire. 6 TDD tests cover nil-tables pass-through, table exclusion, and table inclusion.

### CFG-04: Kaptanto supports operation filtering per table (insert, update, delete)

**Status:** SATISFIED
**Evidence (06-01-SUMMARY.md):** `EventFilter` independently checks an operation allow-list alongside the table allow-list. `nil` operations map means all operations allowed. Tests cover operation exclusion and combined table+operation criteria. Wired in SSEConsumer and GRPCConsumer Deliver methods.

### OBS-01: Kaptanto exposes Prometheus metrics endpoint

**Status:** SATISFIED
**Evidence (06-02-SUMMARY.md):** `KaptantoMetrics` in `internal/observability/metrics.go` uses `prometheus.NewRegistry()` per instance (no global DefaultRegisterer) to prevent double-registration panics in tests. `promhttp.HandlerFor(reg, HandlerOpts{Registry: reg})` serves metrics in text format. Counters include `kaptanto_events_delivered_total` and `kaptanto_errors_total`. Full test suite passes with `CGO_ENABLED=0`.

### OBS-02: Kaptanto exposes /healthz endpoint returning 200/503 with diagnostic JSON

**Status:** SATISFIED
**Evidence (06-02-SUMMARY.md):** `HealthHandler` in `internal/observability/health.go` accepts a `[]HealthProbe` slice at construction time. Returns HTTP 200 when all probes pass; returns HTTP 503 with JSON body listing all failing probes when any probe fails. Tested via `httptest.NewRecorder` (unit) and `httptest.NewServer` (integration) — no live port required.

## CHK-02: Consumer cursors flushed to checkpoint store

**Status:** NOT SATISFIED IN PHASE 6 — Fixed in Phase 7.1

The `SQLiteCursorStore` implemented in plan 06-01 correctly persists cursors using a dirty-map fast path with batched flush. However, `eventlog.LogEntry` lacked a `PartitionID` field at the time Phase 6 was implemented. As a result, `SSEConsumer.Deliver` (06-03) passed a hardcoded `partitionID=0` to `SaveCursor` for every event regardless of which partition it came from. This means cursors for partitions 1..N were stored under partition 0, breaking multi-partition consumer resume correctness.

This defect was noted in 06-03-SUMMARY.md as `// TODO: add PartitionID to eventlog.LogEntry when needed` and was fixed in Phase 7.1, plan 07.1-01, which added `PartitionID uint32` to `LogEntry` and updated `SSEConsumer.Deliver` and `GRPCConsumer.Deliver` to use `entry.PartitionID` when calling `SaveCursor`.

CFG-05 and CFG-06 (column filtering and SQL WHERE filtering) are NOT claimed as satisfied — those are Phase 7.2 work.

## Required Artifacts

| Artifact | Status | Plan |
|----------|--------|------|
| `internal/checkpoint/cursor_store.go` | PRESENT | 06-01 |
| `internal/output/filter.go` | PRESENT | 06-01 |
| `internal/observability/metrics.go` | PRESENT | 06-02 |
| `internal/observability/health.go` | PRESENT | 06-02 |
| `internal/output/sse/consumer.go` | PRESENT | 06-03 |
| `internal/output/sse/server.go` | PRESENT | 06-03 |
| `internal/output/grpc/proto/cdc.proto` | PRESENT | 06-04 |
| `internal/output/grpc/consumer.go` | PRESENT | 06-04 |
| `internal/output/grpc/server.go` | PRESENT | 06-04 |

## Plan Evidence Index

| Plan | Summary | Requirements covered |
|------|---------|---------------------|
| 06-01 | Cursor Store and Event Filter | CHK-02 (partial — defect noted), CFG-03, CFG-04 |
| 06-02 | Observability (Metrics + Health) | OBS-01, OBS-02 |
| 06-03 | SSE Consumer and Server | OUT-02, OUT-03, OUT-04, OUT-05 |
| 06-04 | gRPC Consumer and Server | OUT-06, OUT-07, OUT-08 |

---

_Verified: 2026-03-15_
_Verifier: Claude (gsd-executor, Phase 7.1 infrastructure fixes)_
