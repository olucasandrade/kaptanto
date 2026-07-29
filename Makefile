# Makefile for kaptanto — builds a single static binary with no CGO dependency.

.PHONY: build test test-race verify-no-cgo clean build-rust test-rust \
        lint cover test-integration test-e2e mutation fixtures

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

# test-rust runs the Rust crate's own unit tests, then the rust-tagged Go
# structural-equality suite (parser_ffi_test.go) against the real staticlib.
# Requires: Rust 1.77+, cargo. Host-only, like build-rust — no cross-compile.
test-rust: $(RUST_LIB)
	@echo "[kaptanto] Running cargo test..."
	cd $(RUST_DIR) && cargo test
	@echo "[kaptanto] Running rust-tagged Go structural-equality tests..."
	CGO_ENABLED=1 go test --tags rust ./internal/parser/pgoutput/... -v -count=1

# ---- Quality gates (mirror the CI workflows; run locally before pushing) ----

# lint runs the golangci-lint umbrella: cyclomatic complexity (gocyclo) and
# dependency-structure rules (depguard). Config in .golangci.yml.
lint:
	CGO_ENABLED=0 golangci-lint run ./internal/... ./cmd/...

# cover runs tests with coverage and enforces the coverage gate defined in
# scripts/coverage-gate.sh — the single source of truth shared with
# .github/workflows/coverage.yml (aggregate threshold, exclusions, and the
# per-package floor). Edit thresholds there, not here, so local and CI never
# drift apart again.
cover:
	CGO_ENABLED=0 go test ./internal/... ./cmd/... -count=1 -coverprofile=coverage.out -covermode=count
	@go tool cover -func=coverage.out | tail -1
	@./scripts/coverage-gate.sh coverage.out

# test-integration runs the env-gated Postgres + MongoDB integration tests.
# Requires POSTGRES_TEST_DSN (logical replication) and MONGO_TEST_URI (replica set).
# Without at least one of these, every gated test silently t.Skip()s and this
# target degrades into a duplicate `make test` run that reports success while
# proving nothing — refuse to run rather than produce a false-green result.
test-integration:
	@if [ -z "$$POSTGRES_TEST_DSN" ] && [ -z "$$MONGO_TEST_URI" ]; then \
		echo "ERROR: test-integration requires POSTGRES_TEST_DSN and/or MONGO_TEST_URI to be set." >&2; \
		echo "        Without them, all gated integration tests skip and this target" >&2; \
		echo "        silently duplicates 'make test'. Example:" >&2; \
		echo "        POSTGRES_TEST_DSN=postgres://user:pass@localhost:5432/db MONGO_TEST_URI=mongodb://localhost:27017/?replicaSet=rs0 make test-integration" >&2; \
		exit 1; \
	fi
	CGO_ENABLED=0 go test ./... -count=1 -timeout 300s

# test-e2e runs the black-box binary tests against a live Postgres.
# Requires POSTGRES_TEST_DSN (logical replication).
test-e2e:
	CGO_ENABLED=0 go test -tags e2e ./test/e2e/... -count=1 -timeout 300s -v

# fixtures copies the canonical ChangeEvent NDJSON golden file into the
# TypeScript and Python SDK test trees. CI runs this target and then
# `git diff --exit-code` so uncommitted copies fail the build.
FIXTURE_SRC := internal/event/testdata/changeevent_fixtures.ndjson
FIXTURE_TS  := packages/kaptanto-events/test/fixtures/changeevent_fixtures.ndjson
FIXTURE_PY  := packages/kaptanto-python/tests/fixtures/changeevent_fixtures.ndjson

fixtures:
	@mkdir -p $(dir $(FIXTURE_TS)) $(dir $(FIXTURE_PY))
	cp $(FIXTURE_SRC) $(FIXTURE_TS)
	cp $(FIXTURE_SRC) $(FIXTURE_PY)
	@echo "Copied $(FIXTURE_SRC) → TS + Python test fixtures."
	@git diff --exit-code -- $(FIXTURE_TS) $(FIXTURE_PY) || { \
		echo "ERROR: fixture copies drifted from canonical NDJSON." >&2; \
		echo "       Commit the updated files under packages/*/test(s)/fixtures/." >&2; \
		exit 1; \
	}

# mutation runs gremlins over the core correctness packages. Base config in
# .gremlins.yaml; per-package --threshold-efficacy values below ratchet above
# its 60% fallback toward each package's measured baseline (see .gremlins.yaml
# comment for the baselines and rationale). This target is the single source
# of truth for those thresholds — .github/workflows/mutation.yml invokes it
# directly so CI and local runs cannot drift apart.
#
# parser/pgoutput runs report-only: its FFI shim (ffi_rust.go, //go:build rust)
# is unreachable under the pure-Go test build, which tanks its efficacy score
# as a tooling artifact rather than a real coverage gap. `set -e` still fails
# this target if that gremlins invocation crashes/errors — only its score is
# non-gating.
mutation:
	@set -e; \
	echo "=== gremlins ./internal/router (efficacy >= 90) ==="; \
	gremlins unleash --threshold-efficacy 90 ./internal/router; \
	echo "=== gremlins ./internal/eventlog (efficacy >= 65) ==="; \
	gremlins unleash --threshold-efficacy 65 ./internal/eventlog; \
	echo "=== gremlins ./internal/backfill (efficacy >= 75) ==="; \
	gremlins unleash --threshold-efficacy 75 ./internal/backfill; \
	echo "=== gremlins ./internal/pk (efficacy >= 40) ==="; \
	gremlins unleash --threshold-efficacy 40 ./internal/pk; \
	echo "=== gremlins ./internal/enrich (report-only — ratchet after baseline) ==="; \
	gremlins unleash --threshold-efficacy 0 ./internal/enrich; \
	echo "=== gremlins ./internal/output/vector (report-only — ratchet after baseline) ==="; \
	gremlins unleash --threshold-efficacy 0 ./internal/output/vector; \
	echo "=== gremlins ./internal/action (report-only — ratchet after baseline) ==="; \
	gremlins unleash --threshold-efficacy 0 ./internal/action; \
	echo "=== gremlins ./internal/mcp (report-only — ratchet after baseline) ==="; \
	gremlins unleash --threshold-efficacy 0 ./internal/mcp; \
	echo "=== gremlins ./internal/parser/pgoutput (report-only) ==="; \
	gremlins unleash --threshold-efficacy 0 --threshold-mcover 0 ./internal/parser/pgoutput
