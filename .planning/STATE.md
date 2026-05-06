---
gsd_state_version: 1.0
milestone: v2.1
milestone_name: Queue Sinks
status: unknown
last_updated: "2026-05-06T11:39:30.573Z"
progress:
  total_phases: 30
  completed_phases: 30
  total_plans: 75
  completed_plans: 75
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-05-03)

**Core value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.
**Current focus:** v2.1 — Queue Sinks (roadmap created, ready to plan Phase 19)

## Current Position

Phase: 22 — Google Pub/Sub Sink
Plan: 03 of 03 (complete)
Status: Plan 22-03 complete — case "pubsub": root.go wiring + cmd tests; Phase 22 complete, SNK-04 satisfied
Last activity: 2026-05-06 — Plan 22-03 complete (pubsub root.go wiring + 2 cmd tests)

Progress: [████░░░░░░] 40% (2/5 phases complete, 3/3 plans complete in Phase 22)

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
- [Phase 20 Plan 01]: SQSSinkConfig uses pointer field (*SQSSinkConfig) on SinksConfig — nil when sub-block absent in YAML, consistent with Phase 19 NATS pattern
- [Phase 20 Plan 01]: aws-sdk-go-v2 chosen (v1 deprecated); credentials module included alongside config+sqs for static credential provider in Plan 02
- [Phase 20 Plan 02]: sqsAPI interface extracted from *sqs.Client for unit test injection without live AWS endpoint
- [Phase 20 Plan 02]: newConsumerWithClient internal constructor centralises FIFO validation for both production and test use
- [Phase 20 Plan 02]: Close is a no-op because SQS is stateless HTTP — AWS SDK manages HTTP connection pooling internally
- [Phase 20 Plan 03]: SQS obs server uses cfg.Port (not cfg.Port+1) — SQS publishes to external AWS endpoint; no TCP server binds cfg.Port in SQS mode
- [Phase 20 Plan 03]: Import alias sqssink mirrors natssink convention; reinforces consistent naming pattern for all sink packages
- [Phase 21 Plan 01]: franz-go kept as indirect dependency (no import yet); go mod tidy omitted to preserve entry until Plan 02 imports it
- [Phase 21 Plan 01]: KafkaSinkConfig follows SQSSinkConfig/NATSSinkConfig pointer-field pattern — nil when YAML block absent
- [Phase 21 Plan 03]: kafkasink import alias mirrors natssink/sqssink convention — consistent naming pattern for all sink packages
- [Phase 21 Plan 03]: Kafka obs server listens on cfg.Port (not cfg.Port+1) — Kafka publishes to external broker; no TCP server beyond observability
- [Phase 21 Plan 03]: defer kafkaSink.Close() required — Kafka maintains persistent TCP connections, unlike stateless HTTP SQS sink
- [Phase 22 Plan 01]: pubsub/v2 kept as indirect dependency (no import yet); go mod tidy omitted to preserve entry until Plan 02 imports it — mirrors Phase 21 Plan 01 decision for franz-go
- [Phase 22 Plan 01]: PubSubSinkConfig uses pointer field (*PubSubSinkConfig) on SinksConfig — nil when sub-block absent in YAML, consistent with NATS/SQS/Kafka pattern
- [Phase 22 Plan 01]: CredentialsFile optional; empty = ADC (GOOGLE_APPLICATION_CREDENTIALS or gcloud auth application-default login) — no required credentials beyond ProjectID and TopicID
- [Phase 22 Plan 02]: grpc.NewClient used instead of deprecated grpc.Dial — pstest fake_test.go uses NewClient; cleaner for the codebase
- [Phase 22 Plan 02]: NewPubSubSinkConsumer accepts variadic option.ClientOption — enables pstest injection without a separate internal constructor or interface indirection; production wiring passes no extra options
- [Phase 22 Plan 02]: TopicTemplate preserved in config but not applied per-message — Publisher is created for a fixed topicID at construction (v2 API design); multi-topic support deferred
- [Phase 22 Plan 02]: go mod tidy run in Plan 02 — pubsub/v2 was kept indirect in Plan 01; importing it directly in Plan 02 promotes it and resolves transitive deps
- [Phase 22-google-pubsub-sink]: [Phase 22 Plan 03]: pubsubsink import alias mirrors natssink/sqssink/kafkasink convention — consistent naming pattern for all sink packages
- [Phase 22-google-pubsub-sink]: [Phase 22 Plan 03]: defer pubsubSink.Close() required — PubSubSinkConsumer holds a gRPC connection pool that must be drained on shutdown
- [Phase 22-google-pubsub-sink]: [Phase 22 Plan 03]: Pub/Sub obs server uses cfg.Port (not cfg.Port+1) — Pub/Sub publishes to external GCP endpoint; no TCP server binds cfg.Port in pubsub mode

### Pending Todos

- Plan Phase 19 (run `/gsd:plan-phase 19`)

### Blockers/Concerns

- **Pub/Sub emulator setup (Phase 22):** Exact local integration test harness for Pub/Sub ordering-key correctness not fully resolved. Address at start of Phase 22 planning.
- **RabbitMQ channel pool vs. serialized goroutine (Phase 23):** Research identifies the problem but does not conclusively pick the implementation pattern. Benchmark both approaches during Phase 23 design.
- **NATS JetStream stream pre-creation policy (Phase 19):** RESOLVED — StreamName is optional. If set, validated at startup with fail-fast error. If empty, no validation (user manages stream lifecycle externally).
- **SQS high-throughput FIFO mode (Phase 20):** Whether to enable high-throughput FIFO mode automatically or require explicit opt-in. Decide during Phase 20 planning.

## Session Continuity

Last session: 2026-05-06
Stopped at: Completed 22-03-PLAN.md — PubSub root.go wiring + cmd tests (Phase 22 complete)
Resume file: None
Next action: Execute Phase 23 (RabbitMQ Sink)
