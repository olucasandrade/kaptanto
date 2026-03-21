# Kaptanto Benchmark Harness

Single-command reproducible CDC benchmark comparing Kaptanto against Debezium Server, Sequin, and PeerDB.

## Prerequisites

- Docker >= 24 with Docker Compose v2 (`docker compose` — not `docker-compose`)
- Go 1.25+ (for building the load generator)
- 8 GB RAM available to Docker (PeerDB + Temporal are memory-intensive)
- Ports free: 5432 (Postgres), 3000 (PeerDB UI), 9900 (PeerDB server), 7376 (Sequin)

## Quickstart

```bash
# From repo root
cd bench
docker compose up --build
```

All services reach healthy state within ~2 minutes. The `--build` flag builds the Kaptanto binary from source.

To confirm all services are healthy:

```bash
docker compose ps
```

All entries should show `(healthy)` status.

## Services

| Service | Image | Port | Role |
|---------|-------|------|------|
| postgres | postgres:16.13-alpine | 5432 | Shared CDC source |
| kaptanto | built from source | 7654 (SSE/gRPC), 7655 (metrics) | Kaptanto CDC |
| debezium | quay.io/debezium/server:3.4.2.Final | — | Debezium Server CDC |
| sequin | sequin/sequin:v0.14.6 | 7376 | Sequin CDC |
| peerdb-server | ghcr.io/peerdb-io/peerdb-server:stable-v0.36.12 | 9900 | PeerDB CDC |
| peerdb-ui | ghcr.io/peerdb-io/peerdb-ui:stable-v0.36.12 | 3000 | PeerDB UI |
| redis | redis:7.2.4-alpine | — | Debezium sink + Sequin internal |
| sequin-postgres | postgres:16.13-alpine | — | Sequin metadata store |
| peerdb-postgres | postgres:16.13-alpine | — | PeerDB catalog + Temporal DB |
| temporal | temporalio/auto-setup:1.29 | 7233 | PeerDB orchestration |
| flow-api | ghcr.io/peerdb-io/flow-api:stable-v0.36.12 | — | PeerDB flow control |
| flow-worker | ghcr.io/peerdb-io/flow-worker:stable-v0.36.12 | — | PeerDB flow worker |
| flow-snapshot-worker | ghcr.io/peerdb-io/flow-snapshot-worker:stable-v0.36.12 | — | PeerDB snapshot worker |

## Tool Exclusion: Maxwell's Daemon

Maxwell's Daemon is excluded from this benchmark. It is a MySQL-only CDC tool and does not support Postgres logical replication. This was confirmed by the Maxwell maintainer in issue #434. Only Postgres CDC tools are benchmarked.

## Load Generator

Build and run from the `bench/` directory:

```bash
cd bench
go build -o loadgen ./cmd/loadgen/

# Steady load: 10k rows/sec for 30s (default)
./loadgen --dsn "postgres://bench:bench@localhost:5432/bench" --mode steady

# Burst: ramp 0 to 50k ops/s spike
./loadgen --dsn "..." --mode burst --duration 60s

# Large batch: 100k rows in one transaction
./loadgen --dsn "..." --mode large-batch

# Idle: connectivity check, no writes
./loadgen --dsn "..." --mode idle --duration 30s

# High rate steady
./loadgen --dsn "..." --mode steady --rate 50000 --duration 60s
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dsn` | postgres://bench:bench@localhost:5432/bench | Postgres DSN; env BENCH_DSN also accepted |
| `--rate` | 10000 | Target rows/sec (max effective: 50000) |
| `--mode` | steady | steady, burst, large-batch, or idle |
| `--duration` | 30s | How long to run |
| `--batch-size` | 500 | Rows per CopyFrom batch |
| `--payload-kb` | 1 | Approximate payload bytes per row (KB) |

The loadgen creates the `bench_events` table and `bench_pub` publication automatically on startup.

## PeerDB Source Setup

PeerDB does not read source configuration from environment variables. After `docker compose up`, connect to PeerDB and configure the source peer:

```bash
psql -h localhost -p 9900 -U peerdb -d peerdb
```

```sql
CREATE PEER bench_postgres FROM POSTGRES WITH (
  host = 'postgres',
  port = 5432,
  user = 'bench',
  password = 'bench',
  database = 'bench'
);
```

## Teardown

```bash
docker compose down -v
```

The `-v` flag removes named volumes (including Debezium offset storage and WAL replication slots tracked by Docker).
