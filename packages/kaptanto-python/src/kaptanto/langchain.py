"""LangChain helpers for Kaptanto CDC streams.

Install the optional extra::

    pip install 'kaptanto[langchain]'

LangChain is imported only inside functions so a bare ``pip install kaptanto``
stays free of the dependency.

Reactive agent pattern
----------------------
The preferred continuous-reaction integration is the async stream itself —
no LangChain adapter required beyond your agent runnable::

    from kaptanto import KaptantoStream

    stream = KaptantoStream(
        "http://localhost:7654/events",
        consumer="orders-agent",
        token="...",
        tables=["orders"],
    )
    try:
        async for ev in stream:
            # Any LangChain / LangGraph runnable:
            await agent.ainvoke({"input": ev.model_dump_json()})
    finally:
        await stream.aclose()

``as_tool`` is for the complementary case: an agent *polls* recent CDC
events into its context by invoking a StructuredTool.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from langchain_core.tools import StructuredTool

    from .client import KaptantoStream


def as_tool(
    stream: KaptantoStream,
    *,
    name: str = "kaptanto_recent_events",
    description: str | None = None,
    max_events: int = 50,
    timeout_s: float = 2.0,
) -> StructuredTool:
    """Return a LangChain ``StructuredTool`` that drains recent CDC events.

    Requires ``kaptanto[langchain]`` (``langchain-core>=0.3``). Import of
    LangChain happens lazily inside this function.

    When invoked, the tool collects up to ``max_events`` from ``stream``,
    waiting at most ``timeout_s`` for each next event. Collection stops on
    idle timeout, stream end, or when ``max_events`` is reached. Returns a
    JSON list of ``ChangeEvent`` dicts (``model_dump(mode="json")``).

    Args:
        stream: A live :class:`~kaptanto.client.KaptantoStream`.
        name: Tool name exposed to the LLM.
        description: Tool description; a sensible default is used when omitted.
        max_events: Upper bound on events returned per invocation.
        timeout_s: Idle wait (seconds) for each next event before stopping.

    Raises:
        ImportError: If ``langchain-core`` is not installed.
    """
    try:
        from langchain_core.tools import StructuredTool
    except ImportError as exc:  # pragma: no cover - exercised in unit test
        raise ImportError(
            "kaptanto.langchain.as_tool requires langchain-core. "
            "Install with: pip install 'kaptanto[langchain]'"
        ) from exc

    import asyncio
    import json

    from pydantic import BaseModel

    class _DrainArgs(BaseModel):
        """No inputs — drains whatever events arrive before the idle timeout."""

    desc = description or (
        "Drain recent Kaptanto CDC change events. "
        "Returns a JSON list of ChangeEvent objects "
        "(operation, table, key, before, after, ai_context, ...)."
    )

    async def _drain() -> str:
        events: list[dict[str, Any]] = []
        agen = stream.__aiter__()
        while len(events) < max_events:
            try:
                ev = await asyncio.wait_for(agen.__anext__(), timeout=timeout_s)
            except (TimeoutError, asyncio.TimeoutError, StopAsyncIteration):
                break
            events.append(ev.model_dump(mode="json"))
        return json.dumps(events)

    return StructuredTool.from_function(
        coroutine=_drain,
        name=name,
        description=desc,
        args_schema=_DrainArgs,
    )
