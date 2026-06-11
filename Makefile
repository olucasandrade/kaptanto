# Makefile for kaptanto — builds a single static binary with no CGO dependency.

.PHONY: build test test-race verify-no-cgo clean build-rust \
        lint cover test-integration test-e2e mutation

# Coverage gate threshold (percent). Mirrors COVERAGE_THRESHOLD in
# .github/workflows/coverage.yml. Ratchet upward over time.
COVERAGE_THRESHOLD ?= 50.0

# Version injection — reads from git tag if available, falls back to "dev".
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
  -X 'github.com/olucasandrade/kaptanto/internal/version.Version=$(VERSION)' \
  -X 'github.com/olucasandrade/kaptanto/internal/version.Commit=$(COMMIT)' \
  -X 'github.com/olucasandrade/kaptanto/internal/version.BuildDate=$(BUILD_DATE)'

# Rust FFI acceleration build variables.
RUST_DIR := rust/kaptanto-ffi
RUST_LIB := $(RUST_DIR)/target/release/libkaptanto_ffi.a

# build produces a static binary with no CGO, stripped symbols and debug info.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o kaptanto ./cmd/kaptanto

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
	CGO_ENABLED=1 go build -trimpath -ldflags="$(LDFLAGS)" --tags rust -o kaptanto ./cmd/kaptanto
	@echo "[kaptanto] Built: Rust-accelerated binary -> ./kaptanto"

$(RUST_LIB):
	@echo "[kaptanto] Building Rust static library..."
	cd $(RUST_DIR) && cargo build --release
	@echo "[kaptanto] Rust static library built -> $(RUST_LIB)"

# ---- Quality gates (mirror the CI workflows; run locally before pushing) ----

# lint runs the golangci-lint umbrella: cyclomatic complexity (gocyclo) and
# dependency-structure rules (depguard). Config in .golangci.yml.
lint:
	CGO_ENABLED=0 golangci-lint run ./internal/... ./cmd/...

# cover runs tests with coverage and fails if total is below COVERAGE_THRESHOLD.
cover:
	CGO_ENABLED=0 go test ./internal/... ./cmd/... -count=1 -coverprofile=coverage.out -covermode=count
	@go tool cover -func=coverage.out | tail -1
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/,"",$$3); print $$3}'); \
	awk -v t="$$total" -v min="$(COVERAGE_THRESHOLD)" 'BEGIN { if (t+0 < min+0) { printf "FAIL: coverage %.1f%% < threshold %.1f%%\n", t, min; exit 1 } else { printf "PASS: coverage %.1f%% >= threshold %.1f%%\n", t, min } }'

# test-integration runs the env-gated Postgres + MongoDB integration tests.
# Requires POSTGRES_TEST_DSN (logical replication) and MONGO_TEST_URI (replica set).
test-integration:
	CGO_ENABLED=0 go test ./... -count=1 -timeout 300s

# test-e2e runs the black-box binary tests against a live Postgres.
# Requires POSTGRES_TEST_DSN (logical replication).
test-e2e:
	CGO_ENABLED=0 go test -tags e2e ./test/e2e/... -count=1 -timeout 300s -v

# mutation runs gremlins over the core correctness packages. Config in .gremlins.yaml.
mutation:
	@for pkg in ./internal/router ./internal/eventlog ./internal/parser/pgoutput ./internal/backfill; do \
		echo "=== gremlins $$pkg ==="; \
		gremlins unleash $$pkg || exit 1; \
	done
