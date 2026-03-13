---
phase: 07-configuration-and-multi-source
plan: 01
subsystem: config
tags: [yaml, cobra, config, cli-flags]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: cobra root command with all registered flag names
provides:
  - internal/config package: Config and TableConfig structs
  - Load(path) reads and parses YAML config file
  - Defaults() returns sensible zero-value Config
  - Merge(cfg, cmd) applies only explicitly-set cobra flags onto Config
affects:
  - 07-03-configuration-and-multi-source (root.go wiring uses config.Load + config.Merge)

# Tech tracking
tech-stack:
  added: [gopkg.in/yaml.v3 (promoted from indirect to direct)]
  patterns: [TDD RED-GREEN-REFACTOR, cobra Changed() detection for flag merge]

key-files:
  created:
    - internal/config/config.go
    - internal/config/config_test.go
  modified:
    - go.mod (yaml.v3 promoted to direct dep)

key-decisions:
  - "Retention stored as string not time.Duration — empty string is distinguishable from explicit 0 at runtime"
  - "Merge --tables replaces entire cfg.Tables map with empty TableConfig entries — per-table file config discarded when flag is explicitly set"
  - "No global config variable — callers create Config values and pass them explicitly"
  - "Load() wraps os errors and yaml.Unmarshal errors with context using fmt.Errorf %w"

patterns-established:
  - "cobra Changed() detection: only apply flag value to config when cmd.Flags().Changed(name) is true"
  - "TableConfig nil Columns = all columns (allow-all); non-nil = allow-list"

requirements-completed: [CFG-02]

# Metrics
duration: 2min
completed: 2026-03-13
---

# Phase 7 Plan 1: Config Package — Load, Defaults, Merge Summary

**Typed YAML config package with file loader, sensible defaults, and cobra Changed() flag merge semantics, implemented TDD with 15 tests covering all behavior cases**

## Performance

- **Duration:** ~2 min
- **Started:** 2026-03-12T23:58:20Z
- **Completed:** 2026-03-13T00:00:20Z
- **Tasks:** 1 (TDD: RED + GREEN + go.mod)
- **Files modified:** 3

## Accomplishments
- `TableConfig` struct with `Columns []string` (nil=all columns) and `Where string` (empty=no filter)
- `Config` struct with yaml tags matching locked YAML schema (source, tables, output, port, data-dir, retention)
- `Load()` reads YAML file; returns error for non-existent or malformed files
- `Defaults()` returns Config with output=stdout, port=7654, data-dir=./data, retention="" (empty for runtime default)
- `Merge()` uses cobra `Changed()` to apply only explicitly-set flags; `--tables` replaces entire map, discarding per-table file config
- 15 tests passing, `CGO_ENABLED=0 go build ./...` succeeds, yaml.v3 promoted to direct dep

## Task Commits

Each task was committed atomically:

1. **RED: failing tests** - `9f94730` (test)
2. **GREEN: implementation** - `437eec8` (feat)
3. **go.mod yaml.v3 direct dep** - `004480d` (chore)

## Files Created/Modified
- `internal/config/config.go` — Config, TableConfig, Load, Defaults, Merge
- `internal/config/config_test.go` — 15 TDD tests covering all behavior cases
- `go.mod` — gopkg.in/yaml.v3 promoted from indirect to direct dependency

## Decisions Made
- Retention stored as string (not `time.Duration`) so an empty string is distinguishable from an explicit `0` at runtime — the Event Log initializer applies 1h when empty
- `Merge()` for `--tables` replaces the entire `cfg.Tables` map with empty `TableConfig` entries; any per-table config loaded from file is discarded — flag-level intent is "replicate exactly these tables with no filtering"
- No global config variable; callers create and pass `*Config` explicitly
- `Load()` wraps errors with `fmt.Errorf %w` for caller inspection

## Deviations from Plan

None — plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None — no external service configuration required.

## Next Phase Readiness
- `internal/config` package is ready for Phase 7 Plan 3 (root.go wiring) which calls `config.Load()` + `config.Merge()` in `RunE`
- All exported symbols (`Config`, `TableConfig`, `Load`, `Defaults`, `Merge`) match the interface specified in the plan

---
*Phase: 07-configuration-and-multi-source*
*Completed: 2026-03-13*
