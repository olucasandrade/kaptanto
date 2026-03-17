---
phase: 10-rust-ffi-acceleration
plan: "03"
subsystem: rust-ffi
tags:
  - rust
  - ffi
  - serialization
  - testing
  - cgо
dependency_graph:
  requires:
    - 10-01
    - 10-02
  provides:
    - serializer.rs serde_json helper module
    - TOAST wiring helpers in ffi_rust.go
    - structural equality integration tests
  affects:
    - internal/parser/pgoutput
    - rust/kaptanto-ffi
tech_stack:
  added: []
  patterns:
    - serde_json::Map insertion-order preserving serialization
    - CGO opaque handle pattern for TOAST cache (newToastCache/setToastCache/getToastCache/freeToastCache)
    - structural equality testing: parse JSON and compare fields, never bytes.Equal
key_files:
  created:
    - rust/kaptanto-ffi/src/serializer.rs
    - internal/parser/pgoutput/parser_ffi_test.go
  modified:
    - internal/parser/pgoutput/ffi_rust.go
decisions:
  - "serialize_ordered_fields uses serde_json::Map which preserves insertion order — matches column-index order from pglogrepl for deterministic JSON key ordering on Rust side"
  - "TOAST helpers (newToastCache/set/get/free) added to ffi_rust.go following the CGO pattern from Plan 10-02 — C.CBytes for Go→C copies, C.GoBytes+free for C→Go result"
  - "parser_ffi_test.go uses package pgoutput_test (external test package) to reuse encodeRelation/encodeInsert/encodeUpdate/encodeDelete/tupleCol helpers from parser_test.go without duplication"
  - "Test file has no build tag — verified under CGO_ENABLED=0 (pure-Go, selects ffi_stub.go); same tests work under --tags rust (selects ffi_rust.go)"
  - "Raw byte comparison intentionally omitted — Go map JSON key order is non-deterministic; field-by-field JSON parsing is the correct structural equality criterion"
metrics:
  duration_seconds: 168
  completed_date: "2026-03-17T11:54:16Z"
  tasks_completed: 2
  files_changed: 3
---

# Phase 10 Plan 03: Rust FFI Serializer and Structural Equality Tests Summary

serde_json-backed serializer module, four CGO TOAST wiring helpers, and structural-equality integration tests covering insert/update-with-TOAST/delete/malformed-WAL for both build paths.

## Tasks Completed

### Task 1: Implement serde_json serializer module and finalize TOAST wiring

**Commit:** 0e61060

**Files changed:**
- `rust/kaptanto-ffi/src/serializer.rs` — implemented `serialize_ordered_fields(Vec<(String, Value)>) -> Option<Vec<u8>>` using `serde_json::Map` (insertion-order preserving). Includes inline unit tests for key-order preservation, null values, and empty input.
- `internal/parser/pgoutput/ffi_rust.go` — added four TOAST helper functions: `newToastCache()`, `setToastCache(h, relID, pk, row)`, `getToastCache(h, relID, pk)`, `freeToastCache(h)`. All follow the CGO pattern: `C.CBytes` for Go→C memory copies (with `defer C.free`), `C.GoBytes` + `kaptanto_free_buf` for C→Go result copies. Nil guards on handle parameter.

**Verification:** `cargo build --release` exits 0. `CGO_ENABLED=1 go build --tags rust ./internal/parser/pgoutput/...` exits 0.

### Task 2: Write structural equality integration tests for both build paths

**Commit:** 9f46ed4

**Files changed:**
- `internal/parser/pgoutput/parser_ffi_test.go` — new file with NO build tag. Package `pgoutput_test` (reuses wire encoding helpers from `parser_test.go`). Tests:
  - `TestParserFFI_StructuralEquality_Note` — documents the raw-bytes-not-applicable strategy
  - `TestParserFFI_Insert` — verifies After/Key JSON field values for INSERT (id, email, bio)
  - `TestParserFFI_Update_TOAST` — verifies TOAST column merge: After.bio must equal cached value from prior INSERT
  - `TestParserFFI_Delete` — verifies After=nil and Key contains PK for DELETE
  - `TestParserFFI_MalformedWAL` — verifies (nil, error) return with no panic on malformed bytes

**Verification:** All 5 `TestParserFFI_*` tests pass under `CGO_ENABLED=0`. All 14 total parser tests pass (no regressions).

## Overall Verification

| Check | Result |
|-------|--------|
| `cargo build --release` | PASS |
| `CGO_ENABLED=0 go test ./internal/parser/pgoutput/... -run TestParserFFI -v` | PASS (5/5) |
| `CGO_ENABLED=0 go test ./internal/parser/pgoutput/... -v` | PASS (14/14) |
| `CGO_ENABLED=0 go test ./... -count=1` | PASS (no regressions) |
| `make build` | PASS — pure-Go binary |
| `make build-rust` | PASS — Rust-accelerated binary links end-to-end |

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check: PASSED

Files exist:
- FOUND: rust/kaptanto-ffi/src/serializer.rs
- FOUND: internal/parser/pgoutput/parser_ffi_test.go
- FOUND: internal/parser/pgoutput/ffi_rust.go

Commits exist:
- FOUND: 0e61060 (Task 1)
- FOUND: 9f46ed4 (Task 2)
