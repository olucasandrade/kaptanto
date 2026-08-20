# n8n + Kaptanto Example

Trigger n8n workflows from real-time database changes using the Kaptanto community node.

## What It Shows

- Postgres row changes are captured by Kaptanto and exposed via SSE.
- The `n8n-nodes-kaptanto` trigger node (source: `packages/n8n-nodes-kaptanto/`) connects to Kaptanto's SSE endpoint.
- Any database change instantly triggers your n8n workflow — no polling needed.

## Architecture

```
Postgres → Kaptanto (SSE output) → n8n (KaptantoTrigger node) → Your Workflow
```

## Prerequisites

- Docker & Docker Compose

## Run

```bash
cd examples/n8n-trigger
docker compose up --build
```

Wait for all services to start (n8n installs the community node on first boot, which takes ~30 seconds).

## Services

| Service | URL |
|---------|-----|
| n8n | http://localhost:5678 |
| Kaptanto SSE | http://localhost:7654/events |
| Postgres | localhost:5432 |

## Walkthrough: From INSERT to Workflow Execution

### 1. Set up n8n

Open http://localhost:5678 and complete the initial n8n setup (create an owner account).

### 2. Create a workflow

1. Click **"Add workflow"**.
2. Click the **+** button to add a trigger node.
3. Search for **"Kaptanto"** and select the **Kaptanto Trigger** node.
4. Configure the node:
   - **SSE URL**: `http://kaptanto:7654/events`
   - **Consumer ID**: `n8n-demo`
   - **Tables** (optional): `orders`
5. Click **"Listen for test event"** to arm the trigger.

### 3. Trigger a database change

In a separate terminal:

```bash
psql postgres://postgres:postgres@localhost:5432/app -c \
  "INSERT INTO orders (customer, status, total) VALUES ('carol', 'pending', 75.50);"
```

### 4. Observe the result

Back in n8n, you'll see the trigger fire with the CDC event payload:

```json
{
  "id": "01JXYZ...",
  "table": "public.orders",
  "operation": "insert",
  "key": "3",
  "after": {
    "id": 3,
    "customer": "carol",
    "status": "pending",
    "total": "75.50"
  },
  "ts": "2024-01-01T00:00:00Z",
  "idempotency_key": "..."
}
```

### 5. Build your workflow

Add downstream nodes to do anything with the change event:
- Send a Slack notification
- Update a Google Sheet
- Call an external API
- Insert into another database

## How It Works

The `n8n-nodes-kaptanto` community node uses Kaptanto's SSE endpoint:

1. n8n connects to `http://kaptanto:7654/events?consumer=<id>`.
2. Kaptanto streams CDC events as Server-Sent Events.
3. Each event triggers the workflow with the full `ChangeEvent` payload.
4. The consumer ID ensures n8n receives events from where it left off (durable subscription).

## Filtering

You can filter events in the trigger node configuration:

- **Tables**: Only receive events for specific tables.
- **Operations**: Filter by `insert`, `update`, or `delete`.

Or configure Kaptanto-side filtering in `kaptanto.yaml`:

```yaml
tables:
  public.orders:
    columns: [id, status, total]
    where: "status != 'archived'"
```

## Production Notes

- The `consumer` query parameter provides at-least-once delivery — n8n won't miss events across restarts.
- For auth-protected Kaptanto instances, configure the API credentials in n8n's **Kaptanto API** credential type.
- In production, point the SSE URL to your Kaptanto instance's external address.
