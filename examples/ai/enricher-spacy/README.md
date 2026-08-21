# spaCy Enricher Sidecar

Reference HTTP enricher for Kaptanto's fail-open `ai_context` stage: a small
FastAPI service that runs spaCy `en_core_web_sm` NER and stamps matching CDC
events with entities plus a naive intent derived from operation/table.

## Architecture

```
Postgres (tickets)
  → Kaptanto (enrichment HTTP call before EventLog.Append)
      → enricher sidecar POST /enrich
  → SSE /events (events may include ai_context)
```

## Prerequisites

- Docker & Docker Compose

## Run

```bash
cd examples/ai/enricher-spacy
docker compose up --build
```

First build downloads the spaCy model (~50 MB) and compiles Kaptanto; subsequent
starts are faster. Wait until `enricher` is healthy and Kaptanto is listening on
port 7654.

## Services

| Service | URL |
|---------|-----|
| Enricher | http://localhost:8090/enrich |
| Enricher health | http://localhost:8090/healthz |
| Kaptanto SSE | http://localhost:7654/events |
| Postgres | localhost:5432 (`app` / `postgres` / `postgres`) |

## Walkthrough: INSERT → `ai_context.entities`

### 1. Subscribe to SSE

```bash
curl -N 'http://localhost:7654/events?consumer=enricher-demo&tables=tickets'
```

### 2. Insert a ticket with named entities

In another terminal:

```bash
psql postgres://postgres:postgres@localhost:5432/app -c \
  "INSERT INTO tickets (subject, body) VALUES (
     'Acme Corp invoice',
     'Please call Alice Johnson in New York about the Apple order by Friday.'
   );"
```

### 3. Observe enrichment

The SSE event should include an `ai_context` object similar to:

```json
{
  "intent": "insert_on_tickets",
  "entities": [
    {"type": "ORG", "value": "Acme Corp", "field": "subject"},
    {"type": "PERSON", "value": "Alice Johnson", "field": "body"},
    {"type": "GPE", "value": "New York", "field": "body"},
    {"type": "ORG", "value": "Apple", "field": "body"},
    {"type": "DATE", "value": "Friday", "field": "body"}
  ]
}
```

Exact entity labels depend on spaCy's model; the important part is that
`ai_context.entities` is present and non-empty.

### 4. No extractable text → 204

Rows whose `after`/`before` lack usable text fields get no context. Probe the
sidecar directly:

```bash
curl -i -X POST http://localhost:8090/enrich \
  -H 'Content-Type: application/json' \
  -d '{
    "operation": "insert",
    "table": "tickets",
    "after": {"id": 1, "status": "open"},
    "before": null
  }'
```

Expect `HTTP/1.1 204 No Content` — Kaptanto then appends the event without
`ai_context`.

## Enrichment HTTP contract

| Response | Meaning |
|----------|---------|
| `200` + JSON **object** (≤16 KiB) | Body is stored as `ai_context` on the event |
| `204` | No context; event appends without `ai_context` |
| Anything else (timeout, 5xx, invalid/oversize JSON, non-object) | **Fail open (AIC-01):** event appends unenriched; Kaptanto increments `enrichment_failures_total{reason}` and rate-limits a warn log |

Request: `POST` with `Content-Type: application/json` and the full ChangeEvent body.
Optional `Authorization: Bearer <token>` when `enrichment.auth-token` is set.

Documented `ai_context` shape (opaque to Kaptanto):

```json
{
  "intent": "insert_on_tickets",
  "entities": [{"type": "PERSON", "value": "Alice", "field": "body"}],
  "suggested_actions": [],
  "embedding": {"model": "...", "vector": []},
  "custom": {}
}
```

This sidecar only populates `intent` and `entities`.

## Timeout and fail-open

- Kaptanto's default enrichment timeout is **150ms**. This demo sets
  `enrichment.timeout: 2s` so CPU NER can finish under Docker load.
- On timeout or enricher errors, ingest continues — enrichment never blocks
  checkpoint advance after a successful Append (CHK-01 / AIC-01).
- Enrichment runs **before** `EventLog.Append`, so a crash mid-call re-sends the
  event on restart. The sidecar is **stateless** and must tolerate duplicate
  POSTs (idempotent-tolerant by design).

## Configuration

See `kaptanto.yaml`:

```yaml
enrichment:
  url: "http://enricher:8090/enrich"
  tables: [public.tickets]
  operations: [insert, update]
  timeout: 2s
```

Scope `tables` narrowly: matching events are enriched serially and bound worst-
case ingest to roughly `1/timeout` events/sec for those tables.

## How the sidecar works

1. Read string columns named `subject` / `body` / `title` / `description` /
   `message` / `content` / `text` / `notes`.
2. Run spaCy NER on each selected field.
3. Set `intent` to `{operation}_on_{table}` (naive).
4. Return `204` when no text fields are found.
