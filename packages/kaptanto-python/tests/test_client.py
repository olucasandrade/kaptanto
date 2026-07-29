"""KaptantoStream behavior tests against a local mock SSE server."""

from __future__ import annotations

import json
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any, Callable
from unittest.mock import patch

import pytest

from kaptanto import ChangeEvent, KaptantoStream


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


@pytest.mark.asyncio
async def test_yields_events_in_order(url_builder) -> None:
    ev1 = make_event(id="ev-1", table="orders")
    ev2 = make_event(
        id="ev-2",
        table="users",
        operation="update",
        before={"id": 2},
    )
    ev3 = make_event(
        id="ev-3",
        operation="delete",
        before={"id": 3},
        after=None,
    )

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(sse_frame(ev1))
                self.wfile.write(sse_frame(ev2))
                self.wfile.write(sse_frame(ev3))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="test-svc")
    received: list[ChangeEvent] = []
    async for ev in stream:
        received.append(ev)
        if len(received) == 3:
            await stream.aclose()

    assert [e.id for e in received] == ["ev-1", "ev-2", "ev-3"]


@pytest.mark.asyncio
async def test_yields_ai_context(url_builder) -> None:
    enriched = make_event(
        id="ev-ai",
        ai_context={
            "intent": "fulfill_order",
            "custom": {"priority": "high"},
        },
    )

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(sse_frame(enriched))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="test-svc")
    received: list[ChangeEvent] = []
    async for ev in stream:
        received.append(ev)
        await stream.aclose()

    assert len(received) == 1
    assert received[0].ai_context is not None
    assert received[0].ai_context.intent == "fulfill_order"
    assert received[0].ai_context.custom == {"priority": "high"}


@pytest.mark.asyncio
async def test_passes_query_params(url_builder) -> None:
    seen: dict[str, str] = {}

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                seen["path"] = self.path
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(sse_frame(make_event()))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(
        url,
        consumer="my-svc",
        tables=["orders", "users"],
        operations=["insert", "update"],
    )
    async for _ in stream:
        await stream.aclose()

    assert "consumer=my-svc" in seen["path"]
    assert "tables=orders%2Cusers" in seen["path"] or "tables=orders,users" in seen["path"]
    assert "operations=insert%2Cupdate" in seen["path"] or "operations=insert,update" in seen[
        "path"
    ]


@pytest.mark.asyncio
async def test_sends_authorization_header(url_builder) -> None:
    seen: dict[str, str] = {}

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                seen["auth"] = self.headers.get("Authorization", "")
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(sse_frame(make_event()))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c", token="secret-token-123")
    async for _ in stream:
        await stream.aclose()

    assert seen["auth"] == "Bearer secret-token-123"


@pytest.mark.asyncio
async def test_ignores_ping_comments(url_builder) -> None:
    ev = make_event(id="after-ping")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b": ping\n\n")
                self.wfile.write(b": keep-alive\n\n")
                self.wfile.write(sse_frame(ev))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    async for event in stream:
        received.append(event)
        if len(received) == 1:
            await stream.aclose()

    assert len(received) == 1
    assert received[0].id == "after-ping"


@pytest.mark.asyncio
async def test_skips_malformed_sse_lines(url_builder, caplog: pytest.LogCaptureFixture) -> None:
    ev = make_event(id="good")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b"garbage-no-colon\n\n")
                self.wfile.write(sse_frame(ev))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    with caplog.at_level("WARNING", logger="kaptanto.client"):
        async for event in stream:
            received.append(event)
            if len(received) == 1:
                await stream.aclose()

    assert len(received) == 1
    assert received[0].id == "good"
    assert any("skipping malformed SSE line" in r.message for r in caplog.records)


@pytest.mark.asyncio
async def test_skips_non_json_data(url_builder, caplog: pytest.LogCaptureFixture) -> None:
    ev = make_event(id="valid")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b"data: not-valid-json\n\n")
                self.wfile.write(sse_frame(ev))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    with caplog.at_level("WARNING", logger="kaptanto.client"):
        async for event in stream:
            received.append(event)
            if len(received) == 1:
                await stream.aclose()

    assert len(received) == 1
    assert received[0].id == "valid"
    assert any("skipping non-JSON data" in r.message for r in caplog.records)


@pytest.mark.asyncio
async def test_reconnects_on_disconnect(url_builder) -> None:
    state = {"count": 0}

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                state["count"] += 1
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                # Force connection close so the client sees response end instead
                # of waiting on a persistent HTTP/1.1 connection.
                self.close_connection = True
                self.send_header("Connection", "close")
                self.end_headers()
                if state["count"] == 1:
                    # Abrupt disconnect without a body frame.
                    return
                self.wfile.write(sse_frame(make_event(id="after-reconnect")))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")

    with patch("kaptanto.client._backoff_delay", return_value=0.01):
        received: list[ChangeEvent] = []
        async for event in stream:
            received.append(event)
            if len(received) == 1:
                await stream.aclose()

    assert state["count"] >= 2
    assert received[0].id == "after-reconnect"


@pytest.mark.asyncio
async def test_reconnect_mid_event_at_least_once(url_builder) -> None:
    """G3-19 #18: disconnect mid-frame discards the partial event; reconnect
    may redeliver completed frames (at-least-once) but never yields a corrupt
    or duplicate-beyond-replay event from the torn frame.
    """
    state = {"count": 0}
    complete = make_event(id="complete-before-tear")
    after = make_event(id="after-mid-reconnect")
    # Half of a valid SSE data frame — no trailing \\n\\n, so it stays buffered.
    torn = b'data: {"id":"torn-partial","operation":"insert"'

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                state["count"] += 1
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                # Force connection close so the client sees response end instead
                # of waiting on a persistent HTTP/1.1 connection.
                self.close_connection = True
                self.send_header("Connection", "close")
                self.end_headers()
                if state["count"] == 1:
                    self.wfile.write(sse_frame(complete))
                    self.wfile.flush()
                    self.wfile.write(torn)
                    self.wfile.flush()
                    return  # tear the connection mid-event
                # At-least-once: server may re-send the completed event, then new.
                self.wfile.write(sse_frame(complete))
                self.wfile.write(sse_frame(after))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")

    with patch("kaptanto.client._backoff_delay", return_value=0.01):
        received: list[ChangeEvent] = []
        async for event in stream:
            received.append(event)
            if any(e.id == "after-mid-reconnect" for e in received):
                await stream.aclose()

    assert state["count"] >= 2
    ids = [e.id for e in received]
    assert "after-mid-reconnect" in ids
    assert "torn-partial" not in ids, "partial mid-event frame must never parse"
    # At-least-once: completed event may appear once or twice, never more than
    # the number of successful connection attempts that re-sent it.
    assert 1 <= ids.count("complete-before-tear") <= state["count"]
    assert len(received) <= state["count"] + 1


@pytest.mark.asyncio
async def test_aclose_terminates_promptly(url_builder) -> None:
    import asyncio

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                try:
                    while True:
                        self.wfile.write(b": ping\n\n")
                        self.wfile.flush()
                        time.sleep(0.05)
                except (BrokenPipeError, ConnectionResetError):
                    return

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    start = time.monotonic()

    async def closer() -> None:
        await asyncio.sleep(0.1)
        await stream.aclose()

    closer_task = asyncio.create_task(closer())
    received: list[ChangeEvent] = []
    async for event in stream:
        received.append(event)
    await closer_task

    elapsed = time.monotonic() - start
    assert received == []
    assert elapsed < 3.0


@pytest.mark.asyncio
async def test_ignores_event_and_id_fields(url_builder) -> None:
    ev = make_event(id="with-event-field")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                payload = json.dumps(ev)
                self.wfile.write(f"event: change\nid: 42\ndata: {payload}\n\n".encode())

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    async for event in stream:
        received.append(event)
        if len(received) == 1:
            await stream.aclose()

    assert len(received) == 1
    assert received[0].id == "with-event-field"


@pytest.mark.asyncio
async def test_normalizes_crlf(url_builder) -> None:
    ev = make_event(id="crlf-event")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(f"data: {json.dumps(ev)}\r\n\r\n".encode())

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    async for event in stream:
        received.append(event)
        await stream.aclose()

    assert len(received) == 1
    assert received[0].id == "crlf-event"


@pytest.mark.asyncio
async def test_does_not_retry_terminal_4xx(url_builder) -> None:
    state = {"count": 0}

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                state["count"] += 1
                self.send_response(401)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"unauthorized")

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received: list[ChangeEvent] = []
    async for event in stream:
        received.append(event)

    assert received == []
    assert state["count"] == 1


def test_sync_iter_events(url_builder) -> None:
    ev = make_event(id="sync-1")

    def factory() -> type[BaseHTTPRequestHandler]:
        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(sse_frame(ev))

            def log_message(self, *_args: Any) -> None:
                return

        return H

    url = url_builder(factory)
    stream = KaptantoStream(url, consumer="c")
    received = []
    for event in stream.iter_events():
        received.append(event)
        stream.close()
        break

    assert len(received) == 1
    assert received[0].id == "sync-1"
