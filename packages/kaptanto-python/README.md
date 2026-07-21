# kaptanto

Python SDK for [Kaptanto](https://github.com/olucasandrade/kaptanto) CDC — pydantic
`ChangeEvent` models and an httpx SSE streaming client.

## Install

```bash
pip install kaptanto

# Optional LangChain StructuredTool helper:
pip install 'kaptanto[langchain]'
```

## Models

```python
from kaptanto import ChangeEvent, Operation

raw = {
    "id": "01J5Z0000000000000000000A1",
    "idempotency_key": "pg:public.orders:1:insert:0/1A000001",
    "timestamp": "2026-06-15T12:00:00Z",
    "source": "postgres://cdc@localhost:5432/shop",
    "operation": "insert",
    "table": "orders",
    "key": {"id": 1},
    "before": None,
    "after": {"id": 1, "status": "pending"},
    "metadata": {},
}

ev = ChangeEvent.model_validate(raw)
assert ev.operation is Operation.INSERT
assert ev.is_insert()
```

Field names match the Go `event.ChangeEvent` JSON tags exactly. Optional
`ai_context` carries opaque AI enrichment metadata when present.

## Streaming

```python
import asyncio
from kaptanto import KaptantoStream

async def main() -> None:
    stream = KaptantoStream(
        "http://localhost:7654/events",
        consumer="my-svc",
        token="...",          # optional bearer token
        tables=["orders"],    # optional filter
        operations=["insert", "update"],
    )
    try:
        async for ev in stream:
            print(ev.operation, ev.table, ev.after)
    finally:
        await stream.aclose()

asyncio.run(main())
```

### Sync wrapper

```python
from kaptanto import KaptantoStream

stream = KaptantoStream("http://localhost:7654/events", consumer="my-svc")
for ev in stream.iter_events():
    print(ev.table, ev.operation)
```

The client reconnects with exponential backoff + jitter on disconnect, ignores
SSE comment pings, skips malformed frames with a warning, and resumes via the
stable `consumer` ID (server-side cursor).

## LangChain

LangChain is an **optional** extra. Core `pip install kaptanto` never imports it.

### Reactive agent pattern (preferred)

Wire the stream directly into any LangChain / LangGraph runnable:

```python
from kaptanto import KaptantoStream

stream = KaptantoStream(
    "http://localhost:7654/events",
    consumer="orders-agent",
    token="...",
    tables=["orders"],
)
try:
    async for ev in stream:
        await agent.ainvoke({"input": ev.model_dump_json()})
finally:
    await stream.aclose()
```

### `as_tool` — poll recent events

For agents that *pull* CDC context on demand:

```python
from kaptanto import KaptantoStream
from kaptanto.langchain import as_tool

stream = KaptantoStream("http://localhost:7654/events", consumer="tool-agent")
tool = as_tool(stream, max_events=20, timeout_s=2.0)
# bind `tool` into your agent; each invoke drains recent ChangeEvents as JSON
```

## License

Apache-2.0
