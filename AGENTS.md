# Repository Guidelines

## Project Structure & Module Organization
`cmd/kaptanto` contains the CLI entrypoint. Core Go packages live under `internal/` and are grouped by concern, for example `internal/source/`, `internal/output/`, `internal/parser/`, and `internal/checkpoint/`. Benchmarks and comparison tooling live in `bench/`, with runnable commands under `bench/cmd/` and shared code under `bench/internal/`. The marketing/docs site is a separate Qwik app in `landing/` with routes in `landing/src/routes/`, shared UI in `landing/src/components/`, and static assets in `landing/public/`. Optional Rust FFI code lives in `rust/kaptanto-ffi/`.

## Build, Test, and Development Commands
Use `make build` to compile the main Go binary as `./kaptanto`. Run `make test` for the full Go test suite with `CGO_ENABLED=0`, and `make test-race` for race detection. Use `make verify-no-cgo` before touching build-sensitive code. For the Rust-accelerated path, run `make build-rust`.

For the website, work from `landing/`: `npm run dev` starts the Qwik dev server, `npm run build` creates a production build, `npm run lint` runs ESLint, and `npm run fmt.check` verifies Prettier formatting. For the benchmark harness, use `cd bench && docker compose up --build`.

## Coding Style & Naming Conventions
Follow standard Go formatting with `gofmt`; keep packages small and names lowercase by domain (`eventlog`, `observability`, `backfill`). Tests should stay next to implementation files and use Go’s `_test.go` naming. In `landing/`, use TypeScript, ESLint, and Prettier defaults; keep route files under `src/routes/` and component filenames descriptive and framework-consistent.

## Testing Guidelines
Go tests use the standard `testing` package with `stretchr/testify` available for assertions. Prefer table-driven tests for parser, router, and connector behavior. Run targeted tests with commands such as `go test ./internal/router -v -count=1`. Frontend changes should pass `npm run lint` and `npm run build` in `landing/`.

## Commit & Pull Request Guidelines
Recent history follows Conventional Commit style, for example `feat: ...`, `chore: ...`, and scoped forms like `docs(phase-13): ...`. Keep subjects imperative and specific to one change. PRs should include a short problem statement, implementation summary, test evidence, and linked issues. Add screenshots or short recordings for `landing/` UI changes, and note any required setup for benchmark or replication-related work.
