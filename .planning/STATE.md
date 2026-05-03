---
gsd_state_version: 1.0
milestone: v2.1
milestone_name: Queue Sinks
status: active
last_updated: "2026-05-04T01:22:30Z"
progress:
  total_phases: 27
  completed_phases: 26
  total_plans: 66
  completed_plans: 66
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-05-03)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v2.1 — Queue Sinks (roadmap created, ready to plan Phase 19)

## Current Position

Phase: 19 — Sink Infrastructure and NATS Sink (complete)
Plan: 03 of 03 (complete)
Status: Plan 03 complete — CLI wiring, health probe, /metrics + /healthz for NATS output mode
Last activity: 2026-05-04 — Plan 19-03 complete (NATS CLI wiring in root.go)

Progress: [██░░░░░░░░] 20% (1/5 phases complete, 3/3 plans complete in Phase 19)

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- **Phase 19 first:** Sink infrastructure (config, CLI, metrics, healthz) and NATS sink are built together. NATS is already an indirect dependency and validates the `Deliver` ack-before-nil contract at lowest complexity before other sinks.
- **Sink build order (NATS → SQS → Kafka → Pub/Sub → RabbitMQ):** Ordered simplest-to-hardest connection lifecycle. Each phase adds exactly one new complexity dimension.
- **All cross-cutting requirements in Phase 19:** CFG-01–04, DLV-01–04, OBS-01–02 are all assigned to Phase 19 as the foundation. Each subsequent sink phase inherits this framework.
- **franz-go mandatory for Kafka (Phase 21):** confluent-kafka-go requires CGO and is explicitly excluded. franz-go is the only viable CGO-free Kafka client.
- **pubsub/v2 mandatory for Phase 22:** v1 reaches EOL December 31 2026.
- **RabbitMQ last (Phase 23):** Most complex sink — non-goroutine-safe channels, no auto-reconnect, publisher confirms. Building last means four working sinks and a proven test harness exist first.
- **Plan 19-01: NATS pointer field (*NATSSinkConfig):** Pointer field ensures nil when sinks.nats is absent in YAML; zero-value struct would hide the difference between "not configured" and "empty config".
- **Plan 19-01: No Merge()/Defaults() changes:** Sinks has no CLI flag equivalent in this phase; CFG-04 CLI flag deferred to Plan 03.
- [Phase 19]: isInvalidNATSSubject implements subject validation inline since nats.go v1.51.0 does not export a subject validation function
- [Phase 19]: nc.Flush() after nc.Subscribe() in tests ensures server-side interest registration before JetStream publish to prevent flakiness
- [Phase 19 Plan 03]: NATS obs server uses cfg.Port (not cfg.Port+1) — NATS sink publishes to external broker, no TCP server on cfg.Port
- [Phase 19 Plan 03]: Each queue sink case pattern: nil-check cfg.Sinks.X, construct consumer, SetMetrics, rtr.Register, append HealthProbe, serve /metrics + /healthz

### Pending Todos

- Plan Phase 19 (run `/gsd:plan-phase 19`)

### Blockers/Concerns

- **Pub/Sub emulator setup (Phase 22):** Exact local integration test harness for Pub/Sub ordering-key correctness not fully resolved. Address at start of Phase 22 planning.
- **RabbitMQ channel pool vs. serialized goroutine (Phase 23):** Research identifies the problem but does not conclusively pick the implementation pattern. Benchmark both approaches during Phase 23 design.
- **NATS JetStream stream pre-creation policy (Phase 19):** RESOLVED — StreamName is optional. If set, validated at startup with fail-fast error. If empty, no validation (user manages stream lifecycle externally).
- **SQS high-throughput FIFO mode (Phase 20):** Whether to enable high-throughput FIFO mode automatically or require explicit opt-in. Decide during Phase 20 planning.

## Session Continuity

Last session: 2026-05-04
Stopped at: Completed 19-03-PLAN.md — NATS CLI wiring in root.go
Resume file: None
Next action: Begin Phase 20 (SQS Sink)
