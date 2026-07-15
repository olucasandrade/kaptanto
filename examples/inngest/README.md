# Inngest + Kaptanto Example

Run serverless functions in response to database changes, with built-in deduplication.

## What It Shows

- Postgres row changes are captured by Kaptanto and sent to Inngest as events.
- An Inngest function reacts to `kaptanto/public.orders.update` events.
- Idempotency is automatic: Kaptanto's `idempotency_key` maps to Inngest's `id` field, so retries and crash recovery never trigger duplicate function runs.

## Architecture

```
Postgres → Kaptanto (inngest action) → Inngest Dev Server → Your Functions
```

## Prerequisites

- Docker & Docker Compose
- Node.js 18+ (for the Inngest function server)

## Run

**1. Start infrastructure:**

```bash
cd examples/inngest
docker compose up --build -d
```

This starts Postgres (with seed data), Kaptanto, and the Inngest dev server.

**2. Start the function server:**

```bash
cd inngest
npm install
npm run serve
```

**3. Trigger a change:**

```bash
psql postgres://postgres:postgres@localhost:5432/app -c \
  "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
```

**4. Observe:**

- Inngest Dev UI: http://localhost:8288
- You'll see the `kaptanto/public.orders.update` event arrive and the `on-order-update` function execute.

## Services

| Service | URL |
|---------|-----|
| Inngest Dev UI | http://localhost:8288 |
| Function Server | http://localhost:3000/api/inngest |
| Postgres | localhost:5432 |

## Idempotency & Deduplication

Kaptanto's inngest action type transforms each CDC event into:

```json
{
  "name": "kaptanto/public.orders.update",
  "id": "<idempotency_key>",
  "ts": 1700000000000,
  "data": { "...full ChangeEvent..." }
}
```

The `id` field is Inngest's deduplication key. Because Kaptanto derives it deterministically from the source LSN and table key, the same event always produces the same `id`. If Kaptanto re-delivers after a crash (its checkpoint hadn't advanced), Inngest recognizes the duplicate `id` and skips re-execution.

This means your functions are effectively exactly-once without any application-level dedup logic.

## Configuration

The `kaptanto.yaml` uses the `inngest` action type:

```yaml
actions:
  - name: send-to-inngest
    type: inngest
    params:
      event-key: "local"           # Use your real key in production
      event-name-template: "kaptanto/{{.Table}}.{{.Operation}}"
    routing:
      tables: ["public.orders"]
```

- `event-key`: Your Inngest event key (use `"local"` for the dev server).
- `event-name-template`: Controls the event name. Supports `{{.Table}}`, `{{.Operation}}`, and `{{.Schema}}`.

## Production Notes

- Replace `event-key: "local"` with your real Inngest event key.
- The Inngest action posts to `https://inn.gs/e/<event-key>` in production (no dev server needed).
- Consider filtering to specific operations (e.g., only `update`) via the `routing.operations` field.
