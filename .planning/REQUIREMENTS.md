# Requirements: Kaptanto

**Defined:** 2026-05-03
**Core Value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## v2.1 Requirements

Requirements for the Queue Sinks milestone. Each maps to roadmap phases.

### Queue Sinks

- [ ] **SNK-01**: User can configure Kaptanto to publish CDC events to an AWS SQS FIFO queue
- [ ] **SNK-02**: User can configure Kaptanto to publish CDC events to a RabbitMQ exchange via AMQP
- [ ] **SNK-03**: User can configure Kaptanto to publish CDC events to a Kafka topic
- [ ] **SNK-04**: User can configure Kaptanto to publish CDC events to a Google Pub/Sub topic
- [x] **SNK-05**: User can configure Kaptanto to publish CDC events to a NATS JetStream subject

### Configuration

- [x] **CFG-01**: User can configure sink connection and auth parameters under a `sinks:` YAML block
- [x] **CFG-02**: User can configure per-table topic/queue/subject routing via a Go template (e.g., `cdc.{{.Schema}}.{{.Table}}`)
- [x] **CFG-03**: User can enable TLS for each sink via config
- [x] **CFG-04**: User can select queue sink output via CLI flag (`--output sqs|rabbitmq|kafka|pubsub|nats`)

### Delivery

- [x] **DLV-01**: Each CDC event is delivered at-least-once — `Deliver` blocks until the broker acknowledges receipt
- [x] **DLV-02**: Per-key CDC ordering is preserved end-to-end into the queue (Kafka record key, SQS FIFO `MessageGroupId`, Pub/Sub `OrderingKey`, NATS subject suffix)
- [x] **DLV-03**: Transient broker errors trigger automatic retry via the existing `RetryScheduler`
- [x] **DLV-04**: Each event's `IdempotencyKey` is published as a message attribute/header for consumer-side deduplication

### Observability

- [x] **OBS-01**: Each active sink reports `queue_publish_total`, `queue_publish_errors_total`, and `queue_publish_latency_seconds` Prometheus metrics
- [x] **OBS-02**: Each active sink has a named `/healthz` probe

## Future Requirements

Deferred to v2.2+.

### Throughput

- **TPUT-01**: SQS sink supports `BatchFlusher` for up to 10-message batches per API call
- **TPUT-02**: Kafka sink uses franz-go producer batching for higher throughput

### Routing

- **ROUT-01**: Each sink supports its own per-table filter, independent of the global `tables:` filter
- **ROUT-02**: Dead-letter queue routing for persistent delivery failures

### NATS

- **NATS-01**: NATS sink supports Core NATS mode (at-most-once, opt-in) alongside default JetStream

## Out of Scope

| Feature | Reason |
|---------|--------|
| Exactly-once delivery | Anti-pattern for CDC; at-least-once + `IdempotencyKey` is the correct model |
| Schema registry / Avro serialization | Belongs in a cloud product tier; out of scope for the open-source binary |
| Pub/Sub Lite sink | Separate client library; niche use case |
| RabbitMQ AMQP 1.0 client | Requires RabbitMQ 4.0+ with plugin; AMQP 0-9-1 covers all 3.x and 4.x deployments |
| confluent-kafka-go | Requires CGO, breaks `CGO_ENABLED=0` static binary constraint |
| DLQ routing | Significant config surface area; RTR-04 poison-pill behavior is sufficient for v2.1 |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| CFG-01 | Phase 19 | Complete |
| CFG-02 | Phase 19 | Complete |
| CFG-03 | Phase 19 | Complete |
| CFG-04 | Phase 19 | Complete |
| DLV-01 | Phase 19 | Complete |
| DLV-02 | Phase 19 | Complete |
| DLV-03 | Phase 19 | Complete |
| DLV-04 | Phase 19 | Complete |
| OBS-01 | Phase 19 | Complete |
| OBS-02 | Phase 19 | Complete |
| SNK-05 | Phase 19 | Complete |
| SNK-01 | Phase 20 | Pending |
| SNK-03 | Phase 21 | Pending |
| SNK-04 | Phase 22 | Pending |
| SNK-02 | Phase 23 | Pending |

**Coverage:**
- v2.1 requirements: 15 total
- Mapped to phases: 15
- Unmapped: 0 ✓

---
*Requirements defined: 2026-05-03*
*Last updated: 2026-05-04 — traceability updated after roadmap creation (Phases 19–23)*
