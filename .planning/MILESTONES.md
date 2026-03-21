# Milestones

## v1.2 Benchmark Suite (Shipped: 2026-03-21)

**Phases completed:** 3 phases, 8 plans, 0 tasks

**Key accomplishments:**
- (none recorded)

---

## v1.1 Production Hardening (Shipped: 2026-03-20)

**Phases completed:** 4 phases (8, 9, 9.1, 10), 10 plans
**Timeline:** 2026-03-17 → 2026-03-20 (3 days)
**Git range:** feat(08-high-availability) → docs(10-03)
**Codebase:** 13,873 LOC Go + 336 LOC Rust (14,209 total; +3,460 from v1.0)
**Files changed:** 51 files, 6,982 insertions, 227 deletions

**Key accomplishments:**
- Postgres advisory lock leader election — two instances, exactly one active WAL consumer; standby takes over automatically on leader crash (HA-01, HA-02, HA-03)
- Shared Postgres checkpoint store — new leader resumes from last flushed LSN, zero events skipped on takeover (CHK-05)
- MongoDB Change Streams connector — BSON normalization, resume token persistence, automatic re-snapshot on token expiry (SRC-09–12, PAR-04)
- MongoDB HA guard — `--ha` with MongoDB source returns clear error before pgx connect, INT-03 gap closed (INT-03)
- Rust FFI staticlib crate with cbindgen header and `make build-rust` target — pure Go default build unchanged (PRF-03)
- Rust pgoutput decoder, TOAST cache, serde_json serializer behind `rust` build tag — structural equality tests validate FFI path vs pure Go path (PRF-01)

---

## v1.0 Postgres CDC Binary (Shipped: 2026-03-16)

**Phases completed:** 14 phases (1–7.7), 32 plans
**Timeline:** 2026-03-07 → 2026-03-16 (9 days)
**Codebase:** ~10,749 LOC Go, 114 commits

**Key accomplishments:**
- Full Postgres WAL pipeline — pgoutput decoding, TOAST cache, schema evolution, checkpoint store (SRC-01–08)
- Durable embedded Event Log (Badger v4) — partitioned append, ULID deduplication, configurable TTL
- Consistent backfill engine — keyset cursors, watermark dedup, crash recovery, WAL coordination
- Partitioned router — per-key ordering, consumer isolation, poison pill handling
- Three output modes — stdout NDJSON, SSE with consumer cursors, gRPC with filter predicates
- YAML config — per-table column/row filters, SQL WHERE conditions, multi-source routing
- Prometheus metrics — lag, throughput, backfill progress, errors, consumer lag; /healthz probes

---
