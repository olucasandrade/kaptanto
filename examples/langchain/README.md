# LangChain + Kaptanto Example

Reactive SSE agent: every Postgres change becomes a LangChain `ainvoke` input
via `pip install 'kaptanto[langchain]'`.

## What It Shows

- Kaptanto streams `public.orders` over SSE.
- A Python agent consumes events with `KaptantoStream` and calls `agent.ainvoke`.
- Optional `as_tool` poll/drain pattern for pull-based agents.

## Architecture

```
Postgres → Kaptanto (output: sse) → KaptantoStream → LangChain agent.ainvoke
```

## Prerequisites

- Docker & Docker Compose
- Python 3.10+

## Run

**1. Start infrastructure:**

```bash
cd examples/langchain
docker compose up --build -d
```

**2. Install the SDK extra:**

```bash
pip install 'kaptanto[langchain]'
```

From this monorepo before the package is published:

```bash
pip install -e '../../packages/kaptanto-python[langchain]'
```

**3. Start the reactive agent:**

```bash
export KAPTANTO_URL=http://localhost:7662/events
python agent.py
```

**4. Trigger a change** (separate terminal):

```bash
psql postgres://postgres:postgres@localhost:5442/app -c \
  "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
```

The agent prints the CDC `ChangeEvent` JSON it received.

**5. (Optional) pull-based tool drain:**

```bash
python agent.py tool
```

This uses `kaptanto.langchain.as_tool` to collect recent events into a
LangChain `StructuredTool` result.

## Services

| Service | URL |
|---------|-----|
| Kaptanto SSE | http://localhost:7662/events |
| Postgres | localhost:5442 |

## Reactive pattern (preferred)

```python
from kaptanto import KaptantoStream

stream = KaptantoStream(
    "http://localhost:7662/events",
    consumer="orders-agent",
    tables=["orders"],
)
try:
    async for ev in stream:
        await agent.ainvoke({"input": ev.model_dump_json()})
finally:
    await stream.aclose()
```

Replace `EchoAgent` in `agent.py` with any LangChain / LangGraph runnable that
exposes `ainvoke`.
