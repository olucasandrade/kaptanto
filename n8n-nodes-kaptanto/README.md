# n8n-nodes-kaptanto

[n8n](https://n8n.io/) community node for [Kaptanto](https://github.com/olucasandrade/kaptanto) — trigger workflows from real-time database changes via Change Data Capture (CDC).

## Nodes

### Kaptanto Trigger

Streams real-time CDC events from a Kaptanto instance into your n8n workflows. Each database change (insert, update, delete) becomes an n8n workflow execution.

**Parameters:**

| Parameter | Description |
|---|---|
| Tables | Comma-separated list of tables to watch (e.g. `public.orders, public.users`). Leave empty for all tables. |
| Operations | Filter by operation type: Insert, Update, Delete, Read (Snapshot). Leave empty for all. |
| Consumer ID | Unique identifier for server-side cursor tracking. Defaults to the workflow ID so the stream resumes where it left off across executions. |

## Credentials

### Kaptanto API

| Field | Description |
|---|---|
| Base URL | URL of your Kaptanto instance (e.g. `http://localhost:7654`). The trigger connects to the `/events` SSE endpoint. |
| Auth Token | Bearer token for authentication. Leave empty if Kaptanto is running in insecure mode. |

## Installation

### Community nodes (recommended)

1. Go to **Settings > Community Nodes** in your n8n instance
2. Select **Install a community node**
3. Enter `n8n-nodes-kaptanto`
4. Agree to the risks and install

### Manual installation

```bash
cd ~/.n8n/nodes
npm install n8n-nodes-kaptanto
```

Restart n8n after installation.

## Usage

1. Add a **Kaptanto Trigger** node to your workflow
2. Configure credentials with your Kaptanto instance URL and auth token
3. Set the tables and operations you want to watch
4. Connect downstream nodes to process the CDC events

Each trigger execution receives a single CDC event with this structure:

```json
{
  "id": "01J000000000000000000000AA",
  "idempotency_key": "pg:0/1234:1",
  "timestamp": "2025-01-01T00:00:00Z",
  "source": "postgres",
  "operation": "insert",
  "table": "orders",
  "key": { "id": 1 },
  "before": null,
  "after": { "id": 1, "status": "new" },
  "metadata": {}
}
```

## Features

- **Automatic reconnection** — If the SSE stream drops, the trigger reconnects with exponential backoff
- **Stable cursors** — Consumer ID defaults to the workflow ID, so Kaptanto resumes from where it left off
- **Server-side filtering** — Tables and operations are filtered server-side for efficiency

## Development

```bash
npm install
npm run build
npm test
```

## License

Apache-2.0
