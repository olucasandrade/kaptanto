---
phase: 10-rust-ffi-acceleration
plan: 01
subsystem: infra
tags: [rust, ffi, cgo, cbindgen, staticlib, makefile]

# Dependency graph
requires: []
provides:
  - Rust staticlib crate `kaptanto-ffi` with 6 stub extern C FFI functions
  - cbindgen-generated `rust/kaptanto-ffi/include/kaptanto_ffi.h` C header
  - Makefile `build-rust` target (CGO_ENABLED=1, host-platform only)
  - `RUST_DIR` and `RUST_LIB` Makefile variables for Rust build path
affects: [10-02, 10-03]

# Tech tracking
tech-stack:
  added: [rust/cbindgen 0.26, serde 1, serde_json 1, fnv 1, libc 0.2]
  patterns:
    - Dual-target Makefile: `build` (pure Go, CGO_ENABLED=0) vs `build-rust` (Rust-accelerated, CGO_ENABLED=1)
    - Rust panic=abort in release profile for FFI safety
    - catch_unwind wrapping all extern C entry points

key-files:
  created:
    - rust/kaptanto-ffi/Cargo.toml
    - rust/kaptanto-ffi/build.rs
    - rust/kaptanto-ffi/cbindgen.toml
    - rust/kaptanto-ffi/src/lib.rs
    - rust/kaptanto-ffi/src/decoder.rs
    - rust/kaptanto-ffi/src/toast.rs
    - rust/kaptanto-ffi/src/serializer.rs
    - rust/kaptanto-ffi/.gitignore
  modified:
    - Makefile

key-decisions:
  - "Rust variables (RUST_DIR, RUST_LIB) defined at top of Makefile before use in clean target — ensures correct := immediate-expansion behavior"
  - "build-rust documented as host-platform only — CGO + Rust toolchain requires matching cross-linker; cross-compilation uses make build (pure Go)"
  - "panic=abort in release profile + catch_unwind on all extern C entry points — double safety boundary for FFI"
  - "cbindgen.toml with KAPTANTO_FFI_H include guard — prevents multiple inclusion in CGO consumer files"

patterns-established:
  - "Dual-target Makefile pattern: default build (pure Go, CGO_ENABLED=0) + accelerated build (Rust + CGO); no existing targets modified"
  - "Rust stub pattern: extern C stubs in lib.rs delegate to sub-module functions; Plans 10-02/10-03 implement each module"

requirements-completed: [PRF-03]

# Metrics
duration: 3min
completed: 2026-03-17
---

# Phase 10 Plan 01: Rust FFI Crate Scaffold and Dual-Target Makefile Summary

**Rust staticlib crate with 6 stub extern C FFI functions and cbindgen header generation, plus Makefile `build-rust` target linking Rust static library into Go binary via CGO**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-17T11:39:43Z
- **Completed:** 2026-03-17T11:42:53Z
- **Tasks:** 2
- **Files modified:** 9

## Accomplishments

- Created `rust/kaptanto-ffi/` staticlib crate with `cargo build --release` passing on first attempt
- cbindgen generates `include/kaptanto_ffi.h` on every build with all 6 extern C signatures (`kaptanto_decode_serialize`, `kaptanto_free_buf`, `kaptanto_toast_new`, `kaptanto_toast_set`, `kaptanto_toast_get`, `kaptanto_toast_free`)
- Extended Makefile with `build-rust` target and `RUST_DIR`/`RUST_LIB` variables; pure-Go path (`make build`, `CGO_ENABLED=0`) entirely unchanged
- Full test suite (18 packages) continues to pass with no regressions

## Task Commits

Each task was committed atomically:

1. **Task 1: Create Rust crate scaffold with stub FFI functions** - `cbcbc6a` (feat)
2. **Task 2: Extend Makefile with build-rust target** - `f3bbf56` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified

- `rust/kaptanto-ffi/Cargo.toml` - staticlib crate definition with serde, serde_json, fnv, libc deps; cbindgen build-dep; panic=abort release profile
- `rust/kaptanto-ffi/build.rs` - cbindgen header generation invoked on every cargo build
- `rust/kaptanto-ffi/cbindgen.toml` - C language output, KAPTANTO_FFI_H include guard
- `rust/kaptanto-ffi/src/lib.rs` - 6 extern C stubs with catch_unwind safety boundaries
- `rust/kaptanto-ffi/src/decoder.rs` - stub returning null (Plan 10-02 fills in pgoutput decode)
- `rust/kaptanto-ffi/src/toast.rs` - stub with opaque ToastCache struct (Plan 10-02 fills in HashMap)
- `rust/kaptanto-ffi/src/serializer.rs` - stub placeholder (Plan 10-03 fills in serde_json path)
- `rust/kaptanto-ffi/.gitignore` - excludes /target/
- `Makefile` - added RUST_DIR/RUST_LIB variables, build-rust and $(RUST_LIB) targets, updated clean

## Decisions Made

- Rust variables (`RUST_DIR`, `RUST_LIB`) moved to top of Makefile before `clean` target to ensure correct `:=` immediate-expansion semantics.
- `build-rust` documented as host-platform only with explicit comment explaining why cross-compilation is not supported (CGO requires matching cross-linker for Rust toolchain).
- `panic=abort` in release profile combined with `catch_unwind` on all extern C entry points provides a double safety boundary — Rust panics cannot unwind through the FFI boundary.

## Deviations from Plan

None — plan executed exactly as written. The only structural decision was placing `RUST_DIR`/`RUST_LIB` variable definitions before `clean` (rather than after it as shown in the plan snippet) to ensure correct Make variable expansion order.

## Issues Encountered

None.

## User Setup Required

None — no external service configuration required. Rust toolchain (cargo) and cbindgen are needed for `make build-rust` but the pure-Go path requires nothing new.

## Next Phase Readiness

- Rust crate scaffold ready for Plan 10-02 to implement `decoder.rs` (pgoutput binary decode → JSON) and `toast.rs` (TOAST cache with FNV HashMap)
- Plan 10-03 can implement `serializer.rs` (serde_json acceleration path)
- `include/kaptanto_ffi.h` header ready for Plan 10-02 to create the Go CGO wrapper (`ffi_rust.go`) that imports and calls the C functions
- `make build-rust` target structurally complete; full compilation validation deferred to Plan 10-02 when `ffi_rust.go` CGO file exists

---
*Phase: 10-rust-ffi-acceleration*
*Completed: 2026-03-17*

## Self-Check: PASSED

- All 11 created/modified files found on disk
- Both task commits confirmed in git log: cbcbc6a (Task 1), f3bbf56 (Task 2)
