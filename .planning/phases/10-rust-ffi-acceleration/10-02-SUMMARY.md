---
phase: 10-rust-ffi-acceleration
plan: "02"
subsystem: parser/ffi
tags:
  - rust
  - cgo
  - ffi
  - pgoutput
  - performance
dependency_graph:
  requires:
    - "10-01 (Rust crate scaffold, cbindgen header, Makefile)"
  provides:
    - "ffi_stub.go: pure-Go decodeAndSerializeRow + toastHandle alias"
    - "ffi_rust.go: CGO-backed decodeAndSerializeRow calling kaptanto_decode_serialize"
    - "decoder.rs: length-prefixed binary column decoding + serde_json serialization"
    - "toast.rs: FnvHashMap TOAST cache behind Box::into_raw opaque handle"
  affects:
    - "internal/parser/pgoutput/parser.go (handleInsert/handleUpdate hot paths)"
tech_stack:
  added:
    - "fnv crate (FnvHashMap for TOAST cache — already in Cargo.toml from Plan 10-01)"
  patterns:
    - "Build-tag file pair: ffi_stub.go (!rust) / ffi_rust.go (rust) — one compiles per build"
    - "One CGO call per DML operation: encodeColumns produces full binary buffer before crossing boundary"
    - "Box::into_raw opaque handle: Rust owns TOAST cache memory, Go holds unsafe.Pointer"
    - "base64_encode inlined in decoder.rs: matches Go encoding/json []byte behavior"
key_files:
  created:
    - rust/kaptanto-ffi/src/decoder.rs
    - rust/kaptanto-ffi/src/toast.rs
    - internal/parser/pgoutput/ffi_stub.go
    - internal/parser/pgoutput/ffi_rust.go
  modified:
    - internal/parser/pgoutput/parser.go
decisions:
  - "[10-02]: TOAST cache still maintained via decodeColumns in parser.go for both paths — full Rust TOAST wiring deferred to Plan 10-03 to keep this plan focused on the FFI boundary"
  - "[10-02]: base64_encode inlined in decoder.rs without external crate — Go encoding/json []byte encodes to base64; Rust must match behavior to preserve output identity"
  - "[10-02]: decodeAndSerializeRow signature includes prevRow map[string]any — unused in rust path but required for interface symmetry with ffi_stub.go"
  - "[10-02]: encodeSchema builds JSON array without encoding/json import — avoids import in ffi_rust.go which already has CGO preamble taking import slot"
metrics:
  duration_seconds: 203
  completed_date: "2026-03-17"
  tasks_completed: 2
  files_created: 4
  files_modified: 1
requirements_closed:
  - PRF-01 (partially — decode and TOAST hot paths behind build tag)
---

# Phase 10 Plan 02: Rust FFI Decoder and TOAST Cache Summary

Rust column decoder and Go FFI file-pair wired into parser hot path via build-tag file-pair pattern; one CGO call per DML replaces per-row `decodeColumns` + `json.Marshal`.

## Tasks Completed

| Task | Name | Commit | Key Files |
|------|------|--------|-----------|
| 1 | Implement Rust column decoder and TOAST cache | 17c4608 | rust/kaptanto-ffi/src/decoder.rs, rust/kaptanto-ffi/src/toast.rs |
| 2 | Add FFI file-pair entry points and refactor parser.go | 7b7aaa7 | internal/parser/pgoutput/ffi_stub.go, ffi_rust.go, parser.go |

## What Was Built

### Task 1 — Rust decoder.rs and toast.rs

**decoder.rs** parses the length-prefixed binary wire format (4-byte big-endian column count, then per column: 1-byte type, 4-byte data_len, data bytes). Column types handled:
- `'n'` (null) / `'u'` (TOAST) → `serde_json::Value::Null`
- `'t'` (text) → `serde_json::Value::String` (UTF-8 validated)
- `'b'` (binary) → `serde_json::Value::String` base64-encoded to match Go `encoding/json` behavior

Column order is preserved using `serde_json::Map::from_iter(Vec<(String, Value)>)`, which maintains insertion order for deterministic output.

**toast.rs** replaces the stub with a real `FnvHashMap<(u32, Vec<u8>), Vec<u8>>` behind `Box::into_raw`. All entry points null-guard their raw pointer arguments. `toast_get` returns a heap-allocated copy freed by `kaptanto_free_buf`.

### Task 2 — ffi_stub.go, ffi_rust.go, parser.go refactor

**ffi_stub.go** (`//go:build !rust`): `decodeAndSerializeRow` calls `decodeColumns + json.Marshal`. `toastHandle` type-aliases to `*TOASTCache` for interface symmetry.

**ffi_rust.go** (`//go:build rust`): `encodeColumns` serializes the full column slice into length-prefixed binary in one allocation. `encodeSchema` builds the JSON column-name array without `encoding/json`. `decodeAndSerializeRow` makes a single `C.kaptanto_decode_serialize` call per DML operation. CGO pointer rule is satisfied: column and schema bytes are copied to C-managed memory via `C.CBytes` before the call.

**parser.go** — `handleInsert` and `handleUpdate` hot paths now call `decodeAndSerializeRow` for the after-row JSON. The TOAST cache is still updated via `decodeColumns` (a second decode) for pure-Go path compatibility; Plan 10-03 replaces this with the Rust TOAST handle.

## Verification Results

| Check | Result |
|-------|--------|
| `cargo build --release` exits 0 | PASS |
| `libkaptanto_ffi.a` in target/release | PASS (16.8 MB) |
| `CGO_ENABLED=0 go build ./cmd/kaptanto` | PASS |
| `CGO_ENABLED=0 go test ./internal/parser/pgoutput/... -count=1` | PASS (11/11) |
| `CGO_ENABLED=0 go test ./... -count=1` | PASS (19 packages) |
| `make build-rust` exits 0 | PASS |
| `nm kaptanto | grep kaptanto_decode_serialize` | PASS (symbol present) |

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check: PASSED

- `/Users/lucasandrade/kaptanto/rust/kaptanto-ffi/src/decoder.rs` — FOUND
- `/Users/lucasandrade/kaptanto/rust/kaptanto-ffi/src/toast.rs` — FOUND
- `/Users/lucasandrade/kaptanto/internal/parser/pgoutput/ffi_stub.go` — FOUND
- `/Users/lucasandrade/kaptanto/internal/parser/pgoutput/ffi_rust.go` — FOUND
- `/Users/lucasandrade/kaptanto/internal/parser/pgoutput/parser.go` — FOUND (modified)
- Commit 17c4608 — FOUND
- Commit 7b7aaa7 — FOUND
