"""Reactive LangChain agent driven by Kaptanto SSE CDC events.

Install:

    pip install 'kaptanto[langchain]'

Or from this monorepo (before the package is on PyPI):

    pip install -e '../../packages/kaptanto-python[langchain]'

Run (with examples/langchain compose up):

    export KAPTANTO_URL=http://localhost:7662/events
    python agent.py

Then in another terminal:

    psql postgres://postgres:postgres@localhost:5442/app -c \\
      \"UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;\"
"""

from __future__ import annotations

import asyncio
import os
import sys

from kaptanto import KaptantoStream


class EchoAgent:
    """Stand-in LangChain/LangGraph runnable: ainvoke(input) → print.

    Swap for any real agent that exposes ``ainvoke`` / ``invoke``, e.g.
    ``create_react_agent(...).ainvoke({"messages": [...]})``.
    """

    async def ainvoke(self, payload: dict) -> dict:
        text = payload.get("input", "")
        print(f"[agent] reacted to CDC event:\n{text}\n", flush=True)
        return {"ok": True, "chars": len(text)}


async def run_reactive(agent: EchoAgent) -> None:
    url = os.environ.get("KAPTANTO_URL", "http://localhost:7662/events")
    token = os.environ.get("KAPTANTO_AUTH_TOKEN")  # optional when insecure

    stream = KaptantoStream(
        url,
        consumer="langchain-orders-agent",
        token=token,
        tables=["orders"],
        operations=["insert", "update", "delete"],
    )
    print(f"listening on {url} (consumer=langchain-orders-agent)", flush=True)
    try:
        async for ev in stream:
            # Preferred reactive pattern from the Python SDK README:
            await agent.ainvoke({"input": ev.model_dump_json()})
    finally:
        await stream.aclose()


async def demo_as_tool() -> None:
    """Optional pull-based pattern: drain recent events via StructuredTool."""
    from kaptanto.langchain import as_tool

    url = os.environ.get("KAPTANTO_URL", "http://localhost:7662/events")
    stream = KaptantoStream(url, consumer="langchain-tool-agent", tables=["orders"])
    tool = as_tool(stream, max_events=10, timeout_s=3.0)
    try:
        result = await tool.ainvoke({})
        print(f"[as_tool] drained:\n{result}\n", flush=True)
    finally:
        await stream.aclose()


async def main() -> None:
    mode = sys.argv[1] if len(sys.argv) > 1 else "reactive"
    if mode == "tool":
        await demo_as_tool()
    else:
        await run_reactive(EchoAgent())


if __name__ == "__main__":
    asyncio.run(main())
