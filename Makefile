# Makefile for kaptanto — builds a single static binary with no CGO dependency.

.PHONY: build test test-race verify-no-cgo clean build-rust

# Rust FFI acceleration build variables.
RUST_DIR := rust/kaptanto-ffi
RUST_LIB := $(RUST_DIR)/target/release/libkaptanto_ffi.a

# build produces a static binary with no CGO, stripped symbols and debug info.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o kaptanto ./cmd/kaptanto

# test runs all tests with CGO disabled to enforce the pure-Go build constraint.
# -race requires CGO on some platforms; use test-race for that mode.
test:
	CGO_ENABLED=0 go test ./... -v -count=1

# test-race runs tests with the data-race detector (requires CGO).
test-race:
	go test ./... -v -race -count=1

# verify-no-cgo cross-compiles for linux/amd64 and darwin/arm64 without CGO,
# confirming the entire module compiles as a pure-Go binary.
verify-no-cgo:
	@echo "Verifying pure-Go build for linux/amd64..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
	@echo "Verifying pure-Go build for darwin/arm64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
	@echo "All pure-Go build checks passed."

# clean removes the compiled binary and Rust build artifacts.
clean:
	rm -f kaptanto
	rm -rf $(RUST_DIR)/target

# build-rust: compile Rust static library, then Go binary with rust build tag.
# Requires: Rust 1.77+, cargo, cbindgen (cargo install cbindgen).
# NOTE: Builds for the current host platform only.
#       Cross-compilation is NOT supported on the rust path (CGO + Rust toolchain
#       requires a matching cross-linker for the target platform). Use `make build`
#       for cross-compilation.
build-rust: $(RUST_LIB)
	@echo "[kaptanto] Building Rust-accelerated Go binary (CGO_ENABLED=1)..."
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" --tags rust -o kaptanto ./cmd/kaptanto
	@echo "[kaptanto] Built: Rust-accelerated binary -> ./kaptanto"

$(RUST_LIB):
	@echo "[kaptanto] Building Rust static library..."
	cd $(RUST_DIR) && cargo build --release
	@echo "[kaptanto] Rust static library built -> $(RUST_LIB)"
