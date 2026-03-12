# Phase 7: Configuration and Multi-Source - Context

**Gathered:** 2026-03-13
**Status:** Ready for planning

<domain>
## Phase Boundary

Parse YAML config files (CFG-02), apply column filtering per table (CFG-05), apply SQL WHERE row filtering per table (CFG-06), and wire the full pipeline into a runnable `kaptanto` binary by replacing the root command placeholder with real startup logic.

Creating posts and interactions are separate phases.

</domain>

<decisions>
## Implementation Decisions

### Start command wiring
- Root command runs the pipeline directly — no subcommand. Users type `kaptanto` (not `kaptanto start`)
- CLI flags always win over config file values (12-factor behavior: config file sets defaults, flags override)
- If run with no `--source` and no `--config`, exit with a clear error: `"Error: --source or --config is required"` — do NOT show help or prompt interactively
- Shutdown is graceful: cancel context, drain in-flight events, flush checkpoint + cursor stores, then exit 0 (already satisfies CHK-03)

### YAML schema structure
- Single-source case uses flat top-level keys (no wrapping array):
  ```yaml
  source: postgres://user:pass@host/db
  tables:
    public.orders:
      columns: [id, status, amount]
      where: "status != 'cancelled'"
    public.users:
      columns: [id, email]
  output: stdout
  port: 7654
  data-dir: ./data
  retention: 1h
  ```
- `tables:` is a map, not a list — table name is the key, per-table settings (columns, where) are nested values
- When `--tables` CLI flag is provided, it replaces the config `tables:` map entirely (no per-table config from file applies)
- Column and WHERE filters are config-file-only — no `--columns` or `--where` CLI flags

### Claude's Discretion
- YAML parsing library choice (gopkg.in/yaml.v3 is standard, viper is an alternative)
- How WHERE string is validated at config load time (parse error vs runtime error)
- Internal Config struct design (flat vs nested)
- How column filtering is applied (strip from event JSON before delivery or before EventLog write)
- How SQL WHERE is evaluated (Go-side expression evaluation vs pushing to Postgres query)

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/cmd/root.go`: `NewRootCmd()` factory — `--config` flag already registered; `RunE` is the placeholder to replace
- `internal/output/filter.go`: `EventFilter` (tables + operations) — column filtering is a new concern, additive to this
- `internal/source/postgres/connector.go`: `postgres.Config` struct — the sink for parsed YAML source config
- `internal/checkpoint/`: SQLite store already handles persistence; cursor store is already in place

### Established Patterns
- Cobra CLI: `PersistentPreRunE` already wires logging; new `RunE` should follow same pattern
- All packages already accept config structs — no global state, injection-friendly for the wiring layer
- `CGO_ENABLED=0` is a build invariant — YAML library must be pure Go

### Integration Points
- `root.go RunE` is the entry point for all wiring: parse flags → load config → merge → validate → start pipeline
- `postgres.Config.DSN` is where the source connection string lands after config merge
- `EventFilter` is already consumed by SSE/gRPC consumers — column filter may need a parallel path

</code_context>

<specifics>
## Specific Ideas

- The canonical YAML example (flat top-level, table map with inline columns/where) should become the reference config in docs
- The error for missing source should be actionable: suggest either `--source` flag or `--config` file path

</specifics>

<deferred>
## Deferred Ideas

- None — discussion stayed within phase scope

</deferred>

---

*Phase: 07-configuration-and-multi-source*
*Context gathered: 2026-03-13*
