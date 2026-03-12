---
phase: 06-sse-and-grpc-servers
plan: "03"
subsystem: output/sse
tags: [go, cdc, sse, http, consumer, cursor, tdd]

# Dependency graph
requires:
  - phase: 06-01
    provides: SQLiteCursorStore, EventFilter
  - phase: 06-02
    provides: KaptantoMetrics (optional, used for counters)
  - phase: 05-01
    provides: router.Consumer interface, router.Router, router.ConsumerCursorStore

provides:
  - SSEConsumer implementing router.Consumer
  - SSEServer http.Handler with CORS, ping keepalive, Last-Event-ID resume

---

## One-liner
SSEConsumer and SSEServer ship — browser-native SSE streaming with CORS, 15s ping keepalive, per-consumer cursor persistence, and clean disconnect self-deregistration via permanent error.

## What was built

### Task 1: SSEConsumer (`internal/output/sse/consumer.go`)
- `SSEConsumer` implements `router.Consumer` for a single HTTP connection
- `ID()` returns `"sse:<consumerID>"` — stable across reconnects for cursor lookup
- `Deliver()` writes SSE wire format (`id:`, `data:`, blank line) using `http.NewResponseController` (not deprecated `http.Flusher`)
- Filtered events advance cursor silently (no write to wire)
- Successful delivery saves cursor at `seq+1` via `SQLiteCursorStore`
- Broken pipe / write error is returned directly — `RetryScheduler` classifies as permanent and dead-letters the consumer (no explicit Deregister needed)
- Prometheus `EventsDelivered` metric incremented on each successful delivery

### Task 2: SSEServer (`internal/output/sse/server.go`) + tests
- `SSEServer` is an `http.Handler`; each request creates an independent `SSEConsumer`
- SSE headers set **before** `router.Register()` — prevents wrong Content-Type on first flush
- Query params: `?consumer=<id>&tables=<t1,t2>&operations=<op1,op2>`
- `Last-Event-ID` header is read; cursor loaded by Router via `consumer.ID()` on Register
- Ping ticker sends `": ping\n\n"` every 15s (configurable) to keep proxy connections alive
- Exits cleanly when `r.Context().Done()` fires

### Tests (6 passing)
1. Content-Type: text/event-stream header
2. Access-Control-Allow-Origin: configured-origin header
3. Ping keepalive `: ping` comment sent on interval
4. Context cancellation exits ServeHTTP cleanly
5. Two concurrent consumers are independent (distinct IDs registered)
6. Cursor resume: SSEConsumer with same consumerID starts at persisted seq=42, not seq=1

## Key decisions
- `http.NewResponseController` used throughout (not `w.(http.Flusher)`) — works correctly with `httptest.ResponseRecorder`
- No `Router.Deregister` API needed — permanent error on write self-deregisters via dead-letter path
- `partitionID=0` used throughout (TODO: add `PartitionID` to `eventlog.LogEntry` when needed)

## Self-check
- [x] All tasks executed
- [x] Commits: `a6df68c` (consumer), `633b4aa` (server + tests)
- [x] SUMMARY.md created
- [x] `CGO_ENABLED=0 go test ./internal/output/sse/... -v` → 6/6 PASS
- [x] `CGO_ENABLED=0 go build ./...` → clean
