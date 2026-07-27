"""Async (and sync) SSE client for Kaptanto CDC events."""

from __future__ import annotations

import asyncio
import json
import logging
import random
from collections.abc import AsyncIterator, Iterator
from urllib.parse import parse_qsl, urlencode, urlparse, urlunparse

import httpx
from pydantic import ValidationError

from .models import ChangeEvent, Operation

logger = logging.getLogger("kaptanto.client")

_BASE_DELAY_S = 1.0
_MAX_DELAY_S = 30.0


class _TerminalStreamError(Exception):
    """4xx responses indicate auth/config failure and must not be retried."""


def _backoff_delay(attempt: int) -> float:
    exp = min(_BASE_DELAY_S * (2**attempt), _MAX_DELAY_S)
    return exp * (0.5 + random.random() * 0.5)


def _build_url(
    url: str,
    *,
    consumer: str,
    tables: list[str] | None,
    operations: list[Operation | str] | None,
) -> str:
    parsed = urlparse(url)
    params = dict(parse_qsl(parsed.query, keep_blank_values=True))
    params["consumer"] = consumer
    if tables:
        params["tables"] = ",".join(tables)
    if operations:
        params["operations"] = ",".join(
            op.value if isinstance(op, Operation) else str(op) for op in operations
        )
    return urlunparse(parsed._replace(query=urlencode(params)))


def _is_terminal_status(status: int) -> bool:
    return 400 <= status < 500


class KaptantoStream:
    """Streaming SSE client for Kaptanto CDC events.

    Usage::

        stream = KaptantoStream(
            "http://localhost:7654/events",
            consumer="my-svc",
            token="...",
        )
        async for ev in stream:
            print(ev.operation, ev.table, ev.after)

        # or synchronously:
        for ev in stream.iter_events():
            print(ev.operation)
    """

    def __init__(
        self,
        url: str,
        *,
        token: str | None = None,
        consumer: str,
        tables: list[str] | None = None,
        operations: list[Operation | str] | None = None,
    ) -> None:
        self._url = url
        self._token = token
        self._consumer = consumer
        self._tables = tables
        self._operations = operations
        self._closed = False
        self._client: httpx.AsyncClient | None = None
        self._abort = asyncio.Event()

    async def aclose(self) -> None:
        """Stop reconnecting and close any in-flight HTTP stream."""
        self._closed = True
        self._abort.set()
        if self._client is not None:
            await self._client.aclose()
            self._client = None

    def close(self) -> None:
        """Synchronous close flag; prefer :meth:`aclose` from async code."""
        self._closed = True
        self._abort.set()

    def __aiter__(self) -> AsyncIterator[ChangeEvent]:
        return self._iterate()

    async def _iterate(self) -> AsyncIterator[ChangeEvent]:
        attempt = 0
        self._abort = asyncio.Event()
        self._client = httpx.AsyncClient(timeout=None)
        try:
            while not self._closed:
                try:
                    async for event in self._connect_once():
                        attempt = 0
                        yield event

                    if self._closed:
                        return
                    # Clean end of stream — reconnect with backoff.
                    raise httpx.RemoteProtocolError("SSE stream ended")
                except _TerminalStreamError:
                    return
                except Exception:
                    if self._closed:
                        return
                    delay = _backoff_delay(attempt)
                    attempt += 1
                    try:
                        await asyncio.wait_for(self._abort.wait(), timeout=delay)
                        return
                    except (TimeoutError, asyncio.TimeoutError):
                        continue
        finally:
            if self._client is not None:
                await self._client.aclose()
                self._client = None

    async def _connect_once(self) -> AsyncIterator[ChangeEvent]:
        assert self._client is not None
        headers = {"Accept": "text/event-stream"}
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"

        url = _build_url(
            self._url,
            consumer=self._consumer,
            tables=self._tables,
            operations=self._operations,
        )

        async with self._client.stream("GET", url, headers=headers) as response:
            if response.status_code >= 400:
                msg = (
                    f"SSE request failed: {response.status_code} "
                    f"{response.reason_phrase}"
                )
                if _is_terminal_status(response.status_code):
                    raise _TerminalStreamError(msg)
                raise httpx.HTTPStatusError(
                    msg, request=response.request, response=response
                )

            buffer = ""
            async for chunk in response.aiter_text():
                if self._closed:
                    return
                buffer += chunk.replace("\r\n", "\n")
                frames = buffer.split("\n\n")
                buffer = frames.pop()
                for frame in frames:
                    event = self._parse_frame(frame)
                    if event is not None:
                        yield event

            if buffer.strip() and not self._closed:
                event = self._parse_frame(buffer)
                if event is not None:
                    yield event

    def _parse_frame(self, raw: str) -> ChangeEvent | None:
        data_lines: list[str] = []

        for line in raw.split("\n"):
            if line == "" or line.startswith(":"):
                continue
            if line.startswith("data:"):
                data_lines.append(line[5:].lstrip())
                continue
            if line.startswith(("event:", "id:", "retry:")):
                continue
            logger.warning(
                "KaptantoStream: skipping malformed SSE line lineLength=%s",
                len(line),
            )

        if not data_lines:
            return None

        payload = "\n".join(data_lines)
        try:
            parsed = json.loads(payload)
        except json.JSONDecodeError:
            logger.warning(
                "KaptantoStream: skipping non-JSON data payloadLength=%s",
                len(payload),
            )
            return None

        try:
            return ChangeEvent.model_validate(parsed)
        except ValidationError:
            logger.warning("KaptantoStream: skipping invalid ChangeEvent")
            return None

    def iter_events(self) -> Iterator[ChangeEvent]:
        """Synchronous iterator over ChangeEvents (private event loop)."""

        loop = asyncio.new_event_loop()
        agen = self.__aiter__()

        async def _anext() -> ChangeEvent:
            return await agen.__anext__()

        try:
            while True:
                try:
                    yield loop.run_until_complete(_anext())
                except StopAsyncIteration:
                    break
        finally:
            try:
                loop.run_until_complete(self.aclose())
            except Exception:  # noqa: BLE001 — best-effort cleanup
                self.close()
            loop.close()
