# Mastra + Kaptanto Example

Trigger a Mastra workflow from Postgres `public.orders` changes via Kaptanto SSE and `@kaptanto/mastra`.

## What It Shows

- Kaptanto captures Postgres CDC and exposes events over SSE.
- `@kaptanto/mastra`'s `kaptantoTrigger` starts a Mastra workflow run per event (`createRun()` → `start({ inputData })`).
- `toAgentContext` formats each ChangeEvent into compact agent context (includes `ai_context` when enrichment is enabled).

## Architecture

```
Postgres → Kaptanto (SSE) → @kaptanto/mastra kaptantoTrigger → Mastra workflow
```

## Prerequisites

- Docker & Docker Compose

## Run

```bash
cd examples/ai/mastra
docker compose up --build
```

This starts Postgres (with seed data), Kaptanto, and a Mastra agent container that reacts to `public.orders` changes.

**Trigger a change:**

```bash
psql postgres://postgres:postgres@localhost:5432/app -c \
  "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
```

Watch the `agent` service logs for `[mastra] order change:` lines.

## Local (without Docker for the agent)

```bash
# Terminal 1 — infra
docker compose up --build postgres kaptanto

# Terminal 2 — build packages + run agent
cd ../../../packages/kaptanto-events && npm ci && npm run build
cd ../kaptanto-mastra && npm ci && npm run build
cd ../../examples/ai/mastra && npm install && KAPTANTO_URL=http://localhost:7654/events npm start
```

## Configuration

| Env | Default | Description |
|-----|---------|-------------|
| `KAPTANTO_URL` | `http://kaptanto:7654/events` (compose) | SSE endpoint |
| `KAPTANTO_CONSUMER` | `mastra-orders` | Stable consumer ID for cursor durability |

## Production Notes

- Set a real `--auth-token` / `KAPTANTO_AUTH_TOKEN` and drop `insecure: true`.
- Replace the logging workflow step with an Agent that calls your model using `inputData.context`.
- Use `mapEvent` on `kaptantoTrigger` if your workflow `inputSchema` differs from the default `{ context, event }` shape.
