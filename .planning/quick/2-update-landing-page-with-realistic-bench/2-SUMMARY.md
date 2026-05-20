---
phase: quick-2
plan: "01"
subsystem: landing
tags: [landing, benchmarks, positioning, honesty]
dependency_graph:
  requires: []
  provides: [accurate-benchmark-landing, honest-positioning]
  affects: [landing/src/data/docs-content.ts, landing/src/components/landing/LandingPage.tsx, landing/src/routes/index.tsx]
tech_stack:
  added: []
  patterns: []
key_files:
  modified:
    - landing/src/data/docs-content.ts
    - landing/src/components/landing/LandingPage.tsx
    - landing/src/routes/index.tsx
decisions:
  - "Latency table steady-row values rounded to 3 significant figures matching the planning context (1,100 ms vs raw 1,147 ms) to align the table with the executive summary figures"
  - "Instance sizing callout placed before the cost paragraph in Option 1 so the OOM warning precedes the cost number"
metrics:
  duration: "~2 minutes"
  completed_date: "2026-05-19"
  tasks_completed: 2
  files_modified: 3
---

# Quick Task 2: Update Landing Page with Realistic Benchmark Numbers — Summary

**One-liner:** Corrected landing page with April 2026 clean-run benchmark numbers, honest memory/scope callouts, and removed false "lightweight" and "sub-millisecond" claims.

## What Was Changed

### landing/src/data/docs-content.ts

**docs-benchmarks — Executive Summary table corrections:**

| Cell | Before | After |
|------|--------|-------|
| kaptanto Peak Throughput | 36,267 eps | 4,805 eps (steady) / 36,267 eps (large-batch peak) |
| kaptanto-rust Peak Throughput | 31,883 eps | 3,559 eps (steady) / 31,883 eps (large-batch peak) |
| kaptanto p50 Latency | 1,147 ms | 1.1 s |
| kaptanto-rust p50 Latency | 993 ms | 0.99 s |
| Debezium Peak Throughput | 351 eps | 128 eps (steady) / 150 eps (large-batch) |
| Sequin Peak Throughput | 357 eps | 220 eps (steady) / 324 eps (large-batch) |
| Debezium p50 Latency | 6,004 ms | 34.6 s |
| Sequin p50 Latency | 1,579 ms | 23.6 s |
| kaptanto Infrastructure | 1 binary (Go, ~15 MB) | 1 binary (Go) · 1.1 GB RSS at load |
| kaptanto-rust Infrastructure | 1 binary (Go+Rust FFI, ~15 MB) | 1 binary (Go+Rust FFI) · 1.3 GB RSS at load |
| Debezium Infrastructure | JVM + config files | JVM + Kafka Connect + Kafka broker |
| Sequin Infrastructure | Elixir + Redis + PG | Elixir app + Redis + second RDS instance |

**docs-benchmarks — Added:**
- Memory note callout after executive summary: ~1.1 GB RSS for kaptanto, ~1.3 GB for kaptanto-rust; minimum t3.medium (4 GB)
- Latency table steady-row values rounded (1,147→1,100, 993→990, 34,617→34,600, 23,638→23,600) to match planning context p50/p95/p99 numbers
- "When kaptanto is not the right fit" section: edge/IoT, petabyte-scale ETL, sub-100ms p99 SLAs, large-burst workloads

**docs-aws-setup corrections:**

| Cell | Before | After |
|------|--------|-------|
| Monthly overhead (kaptanto) | ~$15 | ~$85 |
| Monthly overhead (kaptanto-rust) | ~$15 | ~$85 |
| Throughput ceiling (kaptanto) | ~36k eps | ~5k eps steady / 36k eps peak |
| Throughput ceiling (kaptanto-rust) | ~32k eps | ~4k eps steady / 32k eps peak |
| Post-crash drain (kaptanto) | ~30s | ~4s restart / up to 140s p99 drain |
| Post-crash drain (kaptanto-rust) | ~8s | ~3s restart / up to 39s p99 drain |
| Post-crash drain (Debezium) | 145s lag | ~2.7s restart / 145s+ event backlog drain |
| Post-crash drain (Sequin) | 172s lag | 81.8s to re-sync |
| Option 1 ECS Fargate cost | ~$9/mo 0.25 vCPU / 0.5 GB | ~$85/mo 1 vCPU / 4 GB |
| Option 1 total overhead | $10–15/mo | $85–100/mo with EFS |

**docs-aws-setup — Added:**
- Instance sizing callout in Option 1: warns that 0.25 vCPU / 0.5 GB will OOM under production traffic
- "Best use cases for kaptanto" section at top: notification pipelines, live search sync, cache invalidation, audit trail

### landing/src/components/landing/LandingPage.tsx

- Feature card "Sub-millisecond" → "Low-latency streaming" with accurate copy: "Steady-state p50 latency: 1.1s at 10K eps load. No polling interval."
- Footer: "Lightweight CDC for Postgres and MongoDB." → "Simple, fast CDC for Postgres and MongoDB. One binary."
- Added "Use Cases" section (id="use-cases") between Features and Sources sections: 4 cards (notification pipelines, live search sync, cache invalidation, audit trail)

### landing/src/routes/index.tsx

- Meta description: "one lightweight binary" → "one binary, no infrastructure"

## Decisions Made

- Latency table steady-row values rounded to 3 significant figures to align the table with the executive summary rounded figures (1.1s, 0.99s, 34.6s, 23.6s)
- Instance sizing callout placed before the cost paragraph in Option 1 so the OOM warning is visible before the cost number
- "~$15" removed from both kaptanto and kaptanto-rust columns of the AWS comparison table (both updated to $85)

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check

- [x] landing/src/data/docs-content.ts: "1.1 GB" and "1,112" present in benchmarks section
- [x] "4,805" present as steady-state kaptanto eps
- [x] "128" present as Debezium steady eps
- [x] "$85" present in AWS setup kaptanto cost (ECS Fargate task and comparison table)
- [x] "When kaptanto is not the right fit" section exists in docs-benchmarks
- [x] "Best use cases for kaptanto" section exists in docs-aws-setup
- [x] Zero "lightweight" or "Sub-millisecond" hits in LandingPage.tsx and index.tsx
- [x] "use-cases" section id present in LandingPage.tsx

## Self-Check: PASSED

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| Task 1 | 3bf7a81 | fix(landing): update benchmark numbers and add honest positioning in docs-content.ts |
| Task 2 | ce5b60a | fix(landing): remove false lightweight/sub-millisecond claims, add use cases section |
