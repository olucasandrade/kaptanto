---
phase: 06-sse-and-grpc-servers
plan: "04"
subsystem: output/grpc
tags: [go, cdc, grpc, protobuf, channel-bridge, concurrency, tdd]

# Dependency graph
requires:
  - phase: 06-01
    provides: SQLiteCursorStore, EventFilter
  - phase: 06-02
    provides: KaptantoMetrics (optional)
  - phase: 05-01
    provides: router.Consumer interface, router.Router, router.ConsumerCursorStore

provides:
  - CdcStream gRPC service (proto definition + hand-written stubs)
  - GRPCConsumer implementing router.Consumer via channel bridge
  - GRPCServer implementing Subscribe (streaming) and Acknowledge (unary) RPCs

---

## One-liner
GRPCConsumer with channel-bridge pattern and GRPCServer with Subscribe/Acknowledge RPCs ship — HTTP/2 native backpressure (OUT-08) is preserved by calling `stream.Send()` outside all Router locks.

## What was built

### Task 1: Proto definition + stubs (`internal/output/grpc/proto/`)
- `cdc.proto`: `CdcStream` service with `Subscribe` (server-streaming) and `Acknowledge` (unary)
- `ChangeEvent.payload` carries full JSON (OUT-07 JSON fallback via grpc-json codec approach)
- `cdc.pb.go` + `cdc_grpc.pb.go`: hand-written minimal stubs (protoc not available in build env) — marked with `// Code generated` and TODO to regenerate
- `google.golang.org/grpc v1.79.2` added as direct dependency in `go.mod`

### Task 2: GRPCConsumer + GRPCServer (`internal/output/grpc/`)
**Package:** `grpcoutput` (avoids collision with `grpc` import alias)

**GRPCConsumer** (`consumer.go`):
- `ID()` returns `"grpc:<consumerID>"` — stable for cursor lookup
- `Deliver()` encodes event to `proto.ChangeEvent` (JSON payload) and sends to buffered channel (non-blocking)
- If channel full: returns error so `RetryScheduler` backs off (backpressure signal)
- If `done` channel closed: returns error (Subscribe handler exited)
- `Close()` closes `done` channel — called by Subscribe with `defer`

**GRPCServer** (`server.go`):
- `Subscribe()`: creates `GRPCConsumer`, registers with Router, reads `consumer.ch` in loop, calls `stream.Send()` **OUTSIDE** Router lock — prevents deadlock from HTTP/2 backpressure (OUT-08)
- `Acknowledge()`: calls `cursorStore.SaveCursor("grpc:<consumerID>", partitionID, seq)` — persists client-acknowledged cursor
- `NewGRPCNetServer()`: configures `grpc.Server` with 1000 max concurrent streams and keepalive params

### Tests (5 passing)
1. Deliver encodes event to proto.ChangeEvent and sends to buffered channel
2. Channel full returns error (backpressure to RetryScheduler)
3. Subscribe loop delivers events from channel to stream
4. Subscribe exits cleanly on context cancellation
5. Acknowledge calls SaveCursor and returns ok=true

## Key decisions
- Channel bridge pattern: `Deliver()` never calls `stream.Send()` — prevents holding Router lock during HTTP/2 backpressure
- Package name `grpcoutput` (directory `grpc`) avoids stdlib `grpc` import alias collision
- `google.golang.org/grpc v1.79.2` is a **direct** dependency (not indirect)
- Hand-written proto stubs acceptable for Phase 6; regenerate with protoc when CI has it

## Self-check
- [x] All tasks executed
- [x] Commits: `0bffeb7` (proto + grpc dep), `a359c55` (consumer + server + tests)
- [x] SUMMARY.md created
- [x] `CGO_ENABLED=0 go test ./internal/output/grpc/... -v` → 5/5 PASS
- [x] `CGO_ENABLED=0 go build ./...` → clean
- [x] `go.mod` shows `google.golang.org/grpc v1.79.2` as direct dependency
