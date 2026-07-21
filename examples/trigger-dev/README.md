# Trigger.dev + Kaptanto Example

Run durable background tasks from database changes using Trigger.dev v3.

## What It Shows

- Kaptanto captures Postgres CDC events and sends them to Trigger.dev as event triggers.
- A Trigger.dev task processes order updates with durable execution (`wait.forEvent`).
- Works with both Trigger.dev Cloud and self-hosted deployments.

## Architecture

```
Postgres → Kaptanto (triggerdev action) → Trigger.dev API → Your Tasks
```

## Prerequisites

- A Trigger.dev account (cloud) or self-hosted instance
- Node.js 18+
- Postgres with `wal_level=logical`

## Setup

**1. Install dependencies:**

```bash
cd examples/trigger-dev
npm install
```

**2. Configure Kaptanto:**

Export your Trigger.dev secret key (get it from the dashboard under **Project → API Keys**):

```bash
export TRIGGERDEV_API_KEY="tr_dev_YOUR_SECRET_KEY"
```

The `kaptanto.yaml` references it via an environment variable (secret params must never be literal values in the YAML):

```yaml
actions:
  - name: send-to-triggerdev
    type: triggerdev
    params:
      api-key: "${TRIGGERDEV_API_KEY}"
      api-url: "https://api.trigger.dev"  # See "Self-Hosted" below
```

**3. Start Trigger.dev dev mode:**

```bash
npx trigger.dev@latest dev
```

**4. Start Kaptanto:**

```bash
kaptanto --config kaptanto.yaml
```

**5. Trigger a change:**

```bash
psql postgres://postgres:postgres@localhost:5432/app -c \
  "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
```

The `kaptanto-order-update` task will execute in your Trigger.dev dashboard.

## Cloud vs Self-Hosted

### Trigger.dev Cloud (default)

No additional setup needed. The default `api-url` points to `https://api.trigger.dev`:

```yaml
params:
  api-key: "${TRIGGERDEV_API_KEY}"
  # api-url defaults to https://api.trigger.dev
```

Get your secret key from the Trigger.dev dashboard under **Project → API Keys** and export it as `TRIGGERDEV_API_KEY`.

### Self-Hosted

If you're running Trigger.dev on your own infrastructure, set the `api-url` param to your instance:

```yaml
params:
  api-key: "tr_dev_YOUR_SECRET_KEY"
  api-url: "https://trigger.internal.yourcompany.com"
```

Common self-hosted URLs:
- Docker Compose dev: `http://localhost:3030`
- Kubernetes: your ingress hostname

The Trigger.dev self-hosted docs cover deployment options: https://trigger.dev/docs/open-source-self-hosting

## Event Shape

Kaptanto's triggerdev action type transforms each CDC event into:

```json
{
  "event": {
    "name": "kaptanto/public.orders.update",
    "payload": { "...full ChangeEvent..." },
    "id": "<idempotency_key>"
  }
}
```

- `name`: Derived from `event-name-template` — defaults to `kaptanto/{{.Table}}.{{.Operation}}`.
- `payload`: The complete Kaptanto ChangeEvent (typed via `@kaptanto/events`).
- `id`: The idempotency key for dedup on the Trigger.dev side.

## TypeScript Types

Import the `ChangeEvent` type from `@kaptanto/events` for full type safety:

```typescript
import type { ChangeEvent } from "@kaptanto/events";

export const myTask = task({
  id: "process-change",
  run: async (payload: ChangeEvent) => {
    // payload.table, payload.operation, payload.after, etc.
  },
});
```

## Configuration Reference

| Param | Required | Default | Description |
|-------|----------|---------|-------------|
| `api-key` | Yes | — | Trigger.dev secret key (Bearer token) |
| `api-url` | No | `https://api.trigger.dev` | API base URL (for self-hosted) |
| `event-name-template` | No | `kaptanto/{{.Table}}.{{.Operation}}` | Event name pattern |

## Production Notes

- Trigger.dev pinches batch size to 1 (their API doesn't accept arrays).
- The `idempotency_key` in the `id` field prevents duplicate task runs after Kaptanto crash recovery.
- Use `routing.tables` and `routing.operations` to filter which CDC events reach Trigger.dev.
