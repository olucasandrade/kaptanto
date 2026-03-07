---
phase: 01-foundation
plan: 02
subsystem: infra
tags: [go, cobra, cli, makefile, cgo, build, flags]

# Dependency graph
requires:
  - phase: 01-01
    provides: logging.Setup(w, level) used in PersistentPreRunE
provides:
  - kaptanto binary entry point at cmd/kaptanto/main.go calling cmd.Execute()
  - Root cobra command with all 11 persistent CLI flags (source, tables, output, port, config, data-dir, retention, ha, node-id, log-level)
  - Makefile with build, test, test-race, verify-no-cgo, and clean targets
  - Pure-Go build constraint verified via cross-compilation to linux/amd64 and darwin/arm64
affects: [02-event-log, 03-postgres-wal, 04-backfill, 05-router, 06-sse, 07-grpc, 08-config, 09-ha, 10-mongodb]

# Tech tracking
tech-stack:
  added:
    - github.com/spf13/cobra v1.10.2 (CLI framework)
    - github.com/spf13/pflag v1.0.9 (transitive, flag set library)
    - github.com/inconshreveable/mousetrap v1.1.0 (transitive, Windows cobra dep)
  patterns:
    - NewRootCmd() factory for isolated testing — tests call NewRootCmd(), Execute() uses singleton
    - ExecuteWithArgs(args, out) helper for test-friendly output capture
    - PersistentPreRunE for logging initialization before any subcommand
    - RunE no-op placeholder on root (prints help) — future phases add subcommands
    - .gitignore /kaptanto (rooted path) to avoid matching cmd/kaptanto/ directory

key-files:
  created:
    - cmd/kaptanto/main.go
    - internal/cmd/root.go
    - internal/cmd/root_test.go
    - Makefile
  modified:
    - go.mod (added cobra, pflag, mousetrap)
    - go.sum
    - .gitignore (fixed kaptanto → /kaptanto to avoid matching source dir)

key-decisions:
  - "NewRootCmd() factory function exported for test isolation — each test gets a fresh cobra.Command with independent flag set, no global state contamination"
  - "RunE no-op placeholder on root command — without RunE cobra only prints long description, not the flags section; placeholder fixes help output while allowing future subcommand addition"
  - "ExecuteWithArgs(args, out) test helper — cobra defaults to os.Stdout/os.Stderr; test helper sets custom writer so assertions work without capturing os-level stdout"
  - "Retention default 0 at flag layer — applied as 0s at CLI level, 1h default enforced at runtime when event log is initialized"

patterns-established:
  - "cobra command factories: NewRootCmd() for tests, singleton for Execute()"
  - "All binary code under cmd/kaptanto/, all reusable code under internal/"
  - "Makefile is the authoritative build interface — all CI should use make targets"

requirements-completed: [CFG-01, PRF-02]

# Metrics
duration: 3min
completed: 2026-03-07
---

# Phase 1 Plan 2: CLI Entry Point and Makefile Summary

**Cobra CLI skeleton with all 11 required flags, PersistentPreRunE logging initialization, and Makefile verifying pure-Go cross-compilation for linux/amd64 and darwin/arm64**

## Performance

- **Duration:** ~3 min
- **Started:** 2026-03-07T19:58:40Z
- **Completed:** 2026-03-07T20:01:33Z
- **Tasks:** 2
- **Files modified:** 6

## Accomplishments

- Root cobra command with all 11 persistent flags (source, tables, output, port, config, data-dir, retention, ha, node-id, log-level) matching CFG-01 spec
- PersistentPreRunE initializes structured JSON logging from `--log-level` flag before any subcommand runs, hooking into the logging.Setup() built in Plan 01
- Makefile with build/test/test-race/verify-no-cgo/clean targets — CGO_ENABLED=0 enforced throughout
- Cross-compilation to linux/amd64 and darwin/arm64 confirmed (PRF-02 pure-Go constraint)
- 13 tests all passing with CGO_ENABLED=0

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: Failing CLI flag tests** - `4ccc374` (test)
2. **Task 1 GREEN: CLI skeleton implementation** - `64e9660` (feat)
3. **Task 2: Makefile and build verification** - `8ab46b3` (feat)

_Note: TDD tasks have separate RED and GREEN commits per TDD execution flow._

## Files Created/Modified

- `cmd/kaptanto/main.go` - Binary entry point; calls cmd.Execute(), prints error to stderr and exits 1 on failure
- `internal/cmd/root.go` - Root cobra command; NewRootCmd() factory, all 11 persistent flags, PersistentPreRunE, ExecuteWithArgs() test helper
- `internal/cmd/root_test.go` - 13 tests covering all flag names, types, defaults, and help output
- `Makefile` - build/test/test-race/verify-no-cgo/clean targets
- `go.mod` - Added cobra v1.10.2 and transitive deps
- `.gitignore` - Fixed `kaptanto` → `/kaptanto` (rooted) to avoid ignoring cmd/kaptanto/ source directory

## Decisions Made

- **NewRootCmd() factory:** Exported for test isolation. Each test call gets a fresh cobra.Command with an independent flag set. The Execute() function uses a package-level singleton. This prevents flag-registration conflicts and global state leaking between tests.
- **RunE placeholder on root:** Without a RunE/Run defined, cobra only prints the long description when invoked with no subcommand — the Flags section is absent. Adding `RunE: func(cmd, args) error { return cmd.Help() }` makes `--help` and bare invocation show all flags.
- **ExecuteWithArgs test helper:** cobra defaults to writing to os.Stdout and os.Stderr. The helper sets both writers to a provided io.Writer so test assertions on help text work reliably.
- **Retention default 0s:** The flag layer stores 0 as "unset". The actual 1h default is applied at runtime when the Event Log is initialized — this keeps policy out of flag parsing.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed unused import in test file**
- **Found during:** Task 1 GREEN (first test run)
- **Issue:** `root_test.go` imported `"strings"` but did not use it after removing a `strings.Contains` call from an earlier draft
- **Fix:** Removed the unused import
- **Files modified:** `internal/cmd/root_test.go`
- **Verification:** `go test ./internal/cmd/...` compiled and passed
- **Committed in:** `64e9660` (Task 1 GREEN commit)

**2. [Rule 1 - Bug] Fixed .gitignore matching source directory**
- **Found during:** Task 1 GREEN commit staging
- **Issue:** `.gitignore` had bare `kaptanto` which matched the `cmd/kaptanto/` directory, preventing `git add` of source files
- **Fix:** Changed to `/kaptanto` (rooted path) so only the compiled binary at the repo root is ignored
- **Files modified:** `.gitignore`
- **Verification:** `git add cmd/kaptanto/main.go` succeeded after fix
- **Committed in:** `64e9660` (Task 1 GREEN commit)

**3. [Rule 1 - Bug] Added RunE placeholder to root command**
- **Found during:** Task 1 GREEN (testing help output)
- **Issue:** Cobra without Run/RunE only prints long description on `--help`, omitting the flags section. TestHelpContainsAllFlags failed.
- **Fix:** Added `RunE: func(cmd, args) error { return cmd.Help() }` so cobra renders full help including all flags
- **Files modified:** `internal/cmd/root.go`
- **Verification:** All 13 tests pass; `./kaptanto --help` shows all 11 flags
- **Committed in:** `64e9660` (Task 1 GREEN commit)

---

**Total deviations:** 3 auto-fixed (all Rule 1 bugs)
**Impact on plan:** All three fixes were required for correctness. No scope creep.

## Issues Encountered

- cobra without Run/RunE silently truncates help output — not documented in cobra README, only discoverable by testing actual output. Fixed via RunE placeholder pattern.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- `kaptanto --help` shows all 11 CLI flags ready for use by future phases
- Makefile is the authoritative build interface for CI and local development
- Pure-Go constraint verified — all future packages must maintain CGO_ENABLED=0 compatibility
- Logging initialized before any subcommand — all future components receive `*slog.Logger` via dependency injection

---
*Phase: 01-foundation*
*Completed: 2026-03-07*
