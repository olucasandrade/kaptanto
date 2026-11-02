# OpenBot + Kaptanto Example

Turn an explicit Postgres outbox insert into a signed OpenBot routine run. Kaptanto owns durable CDC delivery and retries; a small stateless adapter owns OpenBot's request envelope and signature; OpenBot owns investigation and agent run history.

## What It Shows

- An application requests agent work through an append-only `agent_task_requests` outbox.
- Kaptanto captures only inserts into that table and delivers them with its existing retry/DLQ semantics.
- The adapter accepts only allowlisted task types and forwards a minimal projection—not raw `before`/`after` rows.
- OpenBot receives a signed `kaptanto.agent-task.requested` event and starts the configured routine.
- Duplicate deliveries use the same OpenBot delivery ID, derived from Kaptanto's deterministic idempotency key.

## Architecture

```text
Postgres outbox → Kaptanto custom action → OpenBot signing adapter
                                             ↓
                                    OpenBot webhook routine
                                             ↓
                                  persistent specialist agent
```

The adapter is necessary because Kaptanto's generic webhook signature and OpenBot's webhook signature are different protocols. No Kaptanto core changes are required.

## Security Model

- The outbox is an explicit intent boundary; arbitrary business-table changes do not invoke agents.
- `task_type` must appear in `OPENBOT_ALLOWED_TASK_TYPES`.
- `payload` must be a JSON object and is data, never an agent instruction.
- The adapter requires a bearer token from Kaptanto and does not forward that credential to OpenBot.
- Requests are capped at 64 KiB. Redirects are not followed.
- The adapter logs event IDs, task types, and HTTP statuses, but not payloads or secrets.
- Start with read-only investigation. Any side effect needs separate idempotency and human approval.

## Prerequisites

- Docker with Docker Compose
- An OpenBot account and an online OpenBot host
- A payment-incident runbook and read-only investigation tools if the agent should do more than explain the event

## 1. Create the OpenBot Routine

Create a routine for the specialist agent, either in OpenBot's routine editor or by asking the agent to create a webhook routine:

1. Set **When to run** to **Webhook**.
2. Set the event type to `kaptanto.agent-task.requested`.
3. Optionally filter `/data/taskType` to `payment_failure_triage`.
4. Copy [`routine-instructions.txt`](routine-instructions.txt) into the routine instruction.
5. Copy the routine URL. Generate and copy its one-time signing secret.

OpenBot must remain online to receive requests. If it is offline or busy, the adapter returns the upstream retryable status and Kaptanto retains responsibility for retrying.

## 2. Configure and Start the Stack

```bash
cd examples/integrations/openbot
cp .env.example .env
# Replace every placeholder in .env with the real routine URL and random secrets.
docker compose --env-file .env up --build
```

The source Postgres is exposed on host port `5446`. Kaptanto's optional, outbox-only MCP server is exposed on `7655`; the adapter remains on the Compose network.

## 3. Request an Investigation

In another terminal:

```bash
cd examples/integrations/openbot
docker compose exec postgres psql -U postgres -d app -c "
INSERT INTO agent_task_requests
  (id, task_type, entity_type, entity_id, payload, requested_by)
VALUES
  (gen_random_uuid(), 'payment_failure_triage', 'payment', 'pay_123',
   '{\"attempt\":3,\"error_code\":\"card_declined\"}', 'payment-worker');
"
```

Open the routine history in OpenBot. The run receives only the task reference and approved payload. With no investigation tools configured, it should explain the event and state what evidence is missing rather than inventing a diagnosis.

## Optional MCP Connection

The example enables Kaptanto MCP with access only to the outbox. Register the streamable HTTP server with the OpenBot agent using the same `KAPTANTO_MCP_API_KEY`:

```json
{
  "type": "http",
  "url": "http://localhost:7655",
  "headers": {
    "Authorization": "Bearer <KAPTANTO_MCP_API_KEY>"
  }
}
```

Use MCP for evidence retrieval, not as the wake-up mechanism. The webhook starts the routine; MCP tools such as `get_event_by_id` fetch additional event evidence.

## Delivery Semantics

- Kaptanto delivery is at least once. HTTP `2xx` from OpenBot advances the action cursor.
- OpenBot `200` means the delivery ID was already received; `202` means this delivery was accepted, not that the agent run completed.
- OpenBot `429` and `503` propagate through the adapter and are retryable by Kaptanto. Other upstream `4xx` responses are permanent delivery failures under Kaptanto's webhook policy.
- OpenBot keeps delivery receipts for 7 days. Replays after that window can start another run.
- A later agent failure is visible in OpenBot routine history, not Kaptanto's DLQ. Side effects need their own idempotency checks.

## Adapter Tests

From the repository root:

```bash
go test ./examples/integrations/openbot/adapter
```

The tests cover OpenBot's exact signature bytes, deterministic delivery IDs, payload projection, allowlisting, outbox-only inserts, authentication, and retryable upstream statuses.
