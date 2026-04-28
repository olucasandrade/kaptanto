---
phase: 15-distributed-event-log
plan: "02"
subsystem: infra
tags: [nats, jetstream, cdc, cluster, eventlog, config, cli]

# Dependency graph
requires:
  - phase: 15-01
    provides: NatsEventLog implementation (OpenNats, NatsEventLogConfig, NatsServerConfig, Ping)
  - phase: 14-shared-state-foundation
    provides: PostgresCursorStore, cluster flag wiring pattern in root.go
provides:
  - "--cluster mode opens NatsEventLog instead of BadgerEventLog (EVLOG-03)"
  - "ClusterPeers and NatsClusterPort config fields parsed from YAML and CLI"
  - "Health probe renamed to 'eventlog' — works for both BadgerEventLog and NatsEventLog"
  - "Non-cluster path byte-for-byte identical to pre-Phase-15"
affects: [16-partition-handoff, 17-etcd-coordination, observability, cli-docs]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "elPing func var pattern: capture Ping as func before assigning concrete type to interface, enables health probe dispatch without type assertions"
    - "hostname hoisted before event log branch — shared between NATS Advertise address and cluster heartbeater"

key-files:
  created: []
  modified:
    - internal/config/config.go
    - internal/cmd/root.go

key-decisions:
  - "elPing func variable captures Ping before assigning concrete type to eventlog.EventLog interface — avoids type assertions in health probe"
  - "Health probe name updated from 'badger' to 'eventlog' — neutral label works for both BadgerEventLog and NatsEventLog implementations"
  - "NatsClusterPort=0 → 6222 default applied at pipeline start (not in Defaults()) — preserves distinction between 'not set' and explicitly set"
  - "hostname computed once before event log branch, reused in NATS Advertise address and cluster heartbeater node address"

patterns-established:
  - "elPing pattern: when interface does not expose a method needed by health probe, capture it as func var from concrete type before upcasting"

requirements-completed: [EVLOG-03]

# Metrics
duration: 3min
completed: 2026-04-28
---

# Phase 15 Plan 02: NatsEventLog CLI Wiring Summary

**NatsEventLog wired behind --cluster flag via elPing func pattern; two new CLI flags (--cluster-peers, --nats-cluster-port) route to NATS JetStream cluster on kaptanto start --cluster**

## Performance

- **Duration:** 3 min
- **Started:** 2026-04-28T18:14:55Z
- **Completed:** 2026-04-28T18:17:40Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Added ClusterPeers and NatsClusterPort config fields with YAML tags and Merge handlers for CLI flags
- Replaced single BadgerEventLog open in runPipeline with cluster branch (NatsEventLog when cfg.Cluster=true, BadgerEventLog otherwise)
- Introduced elPing func variable to enable health probe dispatch without type assertions on the EventLog interface
- Renamed /healthz probe from "badger" to "eventlog" — valid for both implementations
- make build, make test, make verify-no-cgo all pass; 25 cmd tests all pass

## Task Commits

Each task was committed atomically:

1. **Task 1: Add ClusterPeers and NatsClusterPort to config.go** - `2910bf5` (feat)
2. **Task 2: Wire NatsEventLog in root.go and add CLI flags** - `6faefab` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified
- `internal/config/config.go` - Added ClusterPeers []string and NatsClusterPort int fields; Merge handlers for --cluster-peers and --nats-cluster-port
- `internal/cmd/root.go` - Added --cluster-peers and --nats-cluster-port flags; cluster branch in step 3 (event log open); elPing func var; /healthz probe renamed to "eventlog"

## Decisions Made
- elPing func variable: EventLog interface does not include Ping() — capture it from the concrete type before upcasting to the interface, then pass elPing to the health probe. This avoids type assertions at each call site.
- Health probe name changed from "badger" to "eventlog": neutral label works for both BadgerEventLog (non-cluster) and NatsEventLog (cluster) without requiring conditional probe registration.
- NatsClusterPort zero-value semantics: Defaults() sets 0 (not 6222) so YAML `nats-cluster-port: 0` is distinguishable from "not set at all". Runtime default applied in runPipeline (0 → 6222).
- Hostname hoisted before event log branch to avoid duplicate os.Hostname() calls — shared by NATS Advertise address and cluster heartbeater.

## Deviations from Plan

None - plan executed exactly as written.

The hostname deduplication (removing the second `hostname, _ := os.Hostname()` declaration inside the `if cfg.Cluster` block at line ~595) was anticipated by the plan ("Reuse the hostname variable that is already computed later in the file") and handled in the same commit.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 15 complete: NatsEventLog implemented (15-01) and wired into CLI (15-02)
- A 3-node cluster can be started with: `kaptanto start --cluster --cluster-dsn ... --cluster-peers node2:6222,node3:6222`
- Non-cluster path is byte-for-byte unchanged — existing single-node deployments unaffected
- Phase 16 (Partition Handoff) can proceed: the NatsEventLog is accessible via the EventLog interface throughout the pipeline

---
*Phase: 15-distributed-event-log*
*Completed: 2026-04-28*
