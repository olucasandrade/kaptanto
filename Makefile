# Makefile for kaptanto — builds a single static binary with no CGO dependency.

.PHONY: build test test-race verify-no-cgo clean

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

# clean removes the compiled binary.
clean:
	rm -f kaptanto
