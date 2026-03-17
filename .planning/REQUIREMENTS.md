# Requirements: Kaptanto

**Defined:** 2026-03-17
**Milestone:** v1.1 Production Hardening
**Core Value:** Every database change is captured and delivered reliably, in order, with zero infrastructure dependencies beyond the database itself.

## v1.1 Requirements

Requirements for the v1.1 milestone. Each maps to roadmap phases.

### High Availability

- [x] **HA-01**: Kaptanto supports leader election via Postgres advisory locks
- [x] **HA-02**: Standby instance polls for lock availability and takes over when primary drops
- [ ] **HA-03**: Active leader loads last checkpoint from shared Postgres store on takeover
- [x] **CHK-05**: Postgres checkpoint store for HA mode (shared state between instances)

### Source Connectors (MongoDB)

- [ ] **SRC-09**: Kaptanto connects to MongoDB via Change Streams on specific collections (MongoDB 4.2+)
- [ ] **SRC-10**: Kaptanto persists MongoDB resume tokens and resumes from last token on restart
- [ ] **SRC-11**: Kaptanto detects expired/invalid resume token and triggers automatic re-snapshot
- [ ] **SRC-12**: Kaptanto handles MongoDB replica set elections transparently via driver

### Parser

- [ ] **PAR-04**: Kaptanto normalizes MongoDB BSON documents into the unified ChangeEvent format

### Performance

- [ ] **PRF-01**: Rust FFI parser accelerates pgoutput decoding, TOAST cache, and JSON serialization behind build tag
- [ ] **PRF-03**: Makefile supports both Go-only and Go+Rust build targets

## Future Requirements

Deferred from v1.1 or carried forward from v1.0.

### Configuration

- **CFG-07**: SIGHUP hot-reload for adding/removing tables without restart
- **CFG-08**: Dynamic table addition via ALTER PUBLICATION

### Operations

- **OPS-01**: Management REST API (GET/POST sources, tables, consumers, backfills)
- **OPS-02**: Badger value log GC on periodic ticker for disk reclamation

### Distribution

- **DST-01**: Docker multi-stage build (Rust -> Go -> scratch)
- **DST-02**: Homebrew tap
- **DST-03**: curl installer script
- **DST-04**: GitHub Actions CI (test, lint, build, release)

## Out of Scope

| Feature | Reason |
|---------|--------|
| Managed sink delivery (webhook, SQS, Kafka, S3) | Reserved for Kaptanto Cloud (SaaS) |
| Web dashboard | CLI + REST API + Grafana is sufficient |
| Transform functions (JavaScript/SQL) | Transforms belong in the consumer |
| Built-in Kafka wire protocol | Too much protocol complexity for a focused binary |
| Long-term retention (30+ days) | Event log is a buffer, not a warehouse |
| MySQL connector | Future database source, not v1.1 |
| Wasm plugins | Premature extensibility |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| HA-01 | Phase 8 | Complete |
| HA-02 | Phase 8 | Complete |
| HA-03 | Phase 8 | Pending |
| CHK-05 | Phase 8 | Complete |
| SRC-09 | Phase 9 | Pending |
| SRC-10 | Phase 9 | Pending |
| SRC-11 | Phase 9 | Pending |
| SRC-12 | Phase 9 | Pending |
| PAR-04 | Phase 9 | Pending |
| PRF-01 | Phase 10 | Pending |
| PRF-03 | Phase 10 | Pending |

**Coverage:**
- v1.1 requirements: 11 total
- Mapped to phases: 11
- Unmapped: 0 ✓

---
*Requirements defined: 2026-03-17*
*Last updated: 2026-03-17 after roadmap creation (phases 8–10 complete)*
