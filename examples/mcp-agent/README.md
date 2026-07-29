# MCP Agent Example

Wire Claude Desktop (or any MCP client) to live Postgres CDC through Kaptanto's
Model Context Protocol server.

## What It Shows

- Kaptanto captures `public.orders` and exposes an MCP streamable-HTTP server.
- An agent discovers tables, subscribes to changes, and drains buffered events.
- Per-key ACL + column redaction (`customer` is masked).

## Architecture

```
Postgres → Kaptanto (mcp.enabled) → MCP tools → Claude Desktop / agent
```

## Prerequisites

- Docker & Docker Compose
- Claude Desktop (or another MCP client that speaks streamable HTTP)

## Run

**1. Set an MCP API key and start infrastructure:**

```bash
cd examples/mcp-agent
export MCP_API_KEY="$(openssl rand -hex 16)"
docker compose up --build -d
```

**2. Point Claude Desktop at Kaptanto:**

Merge `claude_desktop_config.json` into your Claude Desktop MCP config
(typically `~/Library/Application Support/Claude/claude_desktop_config.json` on
macOS). Replace `<MCP_API_KEY>` with the same value you exported in step 1.
Restart Claude Desktop.

**3. Agent workflow — list → subscribe → drain:**

In the MCP client, call tools in this order:

1. **`list_tables`** — returns CDC tables visible to this API key
   (`public.orders` with `captured: true`).
2. **`subscribe_to_changes`** with:
   ```json
   { "tables": ["public.orders"] }
   ```
   → `{ "subscription_id": "...", "resource_uri": "kaptanto://subscriptions/..." }`
3. Trigger a row change:
   ```bash
   psql postgres://postgres:postgres@localhost:5440/app -c \
     "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
   ```
4. **`get_recent_events`** (drain) with:
   ```json
   { "subscription_id": "<id from step 2>", "max": 100 }
   ```
   → `{ "events": [...], "dropped": 0, "remaining": 0 }`

`customer` values appear redacted in drained events; schema still lists the column.

## Services

| Service | URL |
|---------|-----|
| MCP (streamable HTTP) | http://localhost:7655 |
| Observability | http://localhost:7660/healthz |
| Postgres | localhost:5440 |

## Configuration

```yaml
mcp:
  enabled: true
  port: 7655
  api-keys:
    - name: claude-desktop
      key: ${MCP_API_KEY}
      tables: ["public.orders"]
```

- `mcp.enabled: false` (default) costs nothing on the pipeline (MCP-04).
- API key secrets must be `${ENV_VAR}` references.
- `insecure: true` is for local plaintext demos only — use `server-tls` in production.
