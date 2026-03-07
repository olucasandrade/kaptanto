# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kaptanto is an open-source, single Go binary for universal database Change Data Capture (CDC). It streams changes from Postgres (WAL logical replication) and MongoDB (Change Streams) via stdout, SSE, or gRPC. The name means "who captures" in Esperanto.

**Current state:** The technical specification is complete (`kaptanto-technical-specification.md`) and the landing page is live (`landing/`). The Go implementation has not been started yet.

## Repository Structure

- `kaptanto-technical-specification.md` — Complete architecture and implementation spec (authoritative source of truth)
- `landing/` — Static marketing site and docs (vanilla HTML/CSS/JS, no build step)
  - `landing/js/main.js` — All docs content is embedded here as a JS object; routing is client-side

## Implementation Plan

The Go binary is planned as a 6-phase, 24-week roadmap (see spec section 15):

| Phase | Goal |
|---|---|
| 1 | Postgres → stdout (core pipeline) |
| 2 | Event Log (Badger) + backfills |
| 3 | Partitioned router, SSE, gRPC outputs |
| 4 | Multi-source, filtering, YAML config |
| 5 | HA, metrics, management API |
| 6 | MongoDB, Rust FFI, Docker, release |

## Go Architecture (when implementing)

The binary entry point will be `./cmd/kaptanto`. Build with:
```
go build -o kaptanto ./cmd/kaptanto
```

With Rust FFI acceleration (requires Rust 1.77+, CGO):
```
make build-rust
```

**Key Go packages to use** (from spec section 16):
- `jackc/pglogrepl` — Postgres WAL logical replication
- `jackc/pgx/v5` — Postgres driver for snapshots and advisory locks
- `go.mongodb.org/mongo-driver` — MongoDB Change Streams
- `dgraph-io/badger/v4` — embedded Event Log (LSM tree, TTL)
- `google.golang.org/grpc` — gRPC server
- `modernc.org/sqlite` — checkpoint store (pure Go, no CGO)
- `spf13/cobra` — CLI
- `prometheus/client_golang` — metrics
- `oklog/ulid` — sortable event IDs

## Critical Invariants

From the spec — these must never be violated in implementation:

1. **Source checkpoint is NEVER advanced until the event is durably written to the Event Log.** If kaptanto crashes, the source re-sends; the Event Log deduplicates by event ID.

2. **TOAST handling is mandatory.** Postgres may omit unchanged large column values in update events; the parser must merge from a TOAST cache keyed by `(relation_id, primary_key)`.

3. **Keyset cursors, never OFFSET** for snapshot pagination. OFFSET breaks on concurrent inserts/deletes.

4. **Watermark coordination during backfills.** For each snapshot row, check if a WAL event for the same key with a higher LSN already exists in the Event Log; if so, discard the snapshot read.

## Event Schema

All events share a unified JSON format (operation: `insert`, `update`, `delete`, `read`, `control`):
```json
{
  "id": "<ulid>",
  "idempotency_key": "<source>:<schema>.<table>:<pk>:<op>:<position>",
  "operation": "update",
  "table": "orders",
  "key": { "id": 1234 },
  "before": { ... },
  "after": { ... },
  "metadata": { "lsn": "0/1A2B3C4", "checkpoint": "...", "snapshot": false }
}
```

## Landing Page

The landing page (`landing/`) is a single HTML file with no build step. To preview locally, open `landing/index.html` in a browser or serve with any static file server.

All documentation content lives in `landing/js/main.js` as the `docs` object — each key is a doc page ID, each value has `title`, `sub`, and `body` (HTML string). The sidebar structure is defined in the `sidebar` array in the same file. To add or edit docs pages, modify `main.js` directly.
