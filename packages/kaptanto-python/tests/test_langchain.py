"""LangChain as_tool integration tests (requires kaptanto[langchain])."""

from __future__ import annotations

import json
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any, Callable

import pytest

langchain_core = pytest.importorskip("langchain_core")

from kaptanto import KaptantoStream  # noqa: E402
from kaptanto.langchain import as_tool  # noqa: E402


def make_event(**overrides: Any) -> dict[str, Any]:
    base: dict[str, Any] = {
        "id": "01J000000000000000000000AA",
        "idempotency_key": "pg:0/1234:1",
        "timestamp": "2025-01-01T00:00:00Z",
        "source": "postgres",
        "operation": "insert",
        "table": "orders",
        "key": {"id": 1},
        "before": None,
        "after": {"id": 1, "status": "new"},
        "metadata": {},
    }
    base.update(overrides)
    return base


def sse_frame(data: Any) -> bytes:
    return f"data: {json.dumps(data)}\n\n".encode()


class _SSEServer:
    def __init__(self, handler_factory: Callable[[], type[BaseHTTPRequestHandler]]) -> None:
        self._handler_cls = handler_factory()
        self.server = HTTPServer(("127.0.0.1", 0), self._handler_cls)
        self.port = self.server.server_address[1]
        self._thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self._thread.join(timeout=2)


@pytest.fixture
def url_builder():
    servers: list[_SSEServer] = []

    def start(handler_factory: Callable[[], type[BaseHTTPRequestHandler]]) -> str:
        srv = _SSEServer(handler_factory)
        srv.start()
        servers.append(srv)
        return f"http://127.0.0.1:{srv.port}/events"

    yield start

    for srv in servers:
        srv.stop()


def _hold_open_handler(events: list[dict[str, Any]]) -> type[BaseHTTPRequestHandler]:
    """SSE handler that emits events then pauses before closing.

    The brief hold lets the drain idle-timeout complete without the client
    reconnecting and re-reading the same frames. The handler must return so
    HTTPServer.shutdown() is not blocked (handlers run on the serve thread).
    """

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            # Force connection close so the client sees response end instead
            # of waiting on a persistent HTTP/1.1 connection.
            self.close_connection = True
            self.send_header("Connection", "close")
            self.end_headers()
            for ev in events:
                self.wfile.write(sse_frame(ev))
                self.wfile.flush()
            # Stay open past the tool idle timeout, then return.
            time.sleep(2.5)

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A003
            return

    return Handler


@pytest.mark.asyncio
async def test_as_tool_drains_recent_events(url_builder) -> None:
    from langchain_core.tools import StructuredTool

    events = [
        make_event(id="ev-1", table="orders"),
        make_event(id="ev-2", table="users", operation="update", before={"id": 2}),
    ]

    url = url_builder(lambda: _hold_open_handler(events))
    stream = KaptantoStream(url, consumer="langchain-tool")
    try:
        tool = as_tool(stream, max_events=10, timeout_s=2.0)
        assert isinstance(tool, StructuredTool)
        assert tool.name == "kaptanto_recent_events"

        raw = await tool.ainvoke({})
        parsed = json.loads(raw)
        assert isinstance(parsed, list)
        assert len(parsed) == 2
        assert parsed[0]["id"] == "ev-1"
        assert parsed[0]["table"] == "orders"
        assert parsed[1]["id"] == "ev-2"
        assert parsed[1]["operation"] == "update"
    finally:
        await stream.aclose()


@pytest.mark.asyncio
async def test_as_tool_respects_max_events(url_builder) -> None:
    events = [make_event(id=f"ev-{i}") for i in range(5)]

    url = url_builder(lambda: _hold_open_handler(events))
    stream = KaptantoStream(url, consumer="langchain-max")
    try:
        tool = as_tool(stream, name="drain_two", max_events=2, timeout_s=2.0)
        assert tool.name == "drain_two"
        raw = await tool.ainvoke({})
        parsed = json.loads(raw)
        assert len(parsed) == 2
        assert [e["id"] for e in parsed] == ["ev-0", "ev-1"]
    finally:
        await stream.aclose()
