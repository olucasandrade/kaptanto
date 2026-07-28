import { createServer, type Server, type IncomingMessage, type ServerResponse } from "node:http";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { KaptantoStream } from "../src/client.js";
import type { ChangeEvent } from "../src/types.js";

function makeEvent(overrides: Partial<ChangeEvent> = {}): ChangeEvent {
  return {
    id: "01J000000000000000000000AA",
    idempotency_key: "pg:0/1234:1",
    timestamp: "2025-01-01T00:00:00Z",
    source: "postgres",
    operation: "insert",
    table: "orders",
    key: { id: 1 },
    before: null,
    after: { id: 1, status: "new" },
    metadata: {},
    ...overrides,
  };
}

function sseFrame(data: unknown): string {
  return `data: ${JSON.stringify(data)}\n\n`;
}

function listen(server: Server): Promise<number> {
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      resolve(typeof addr === "object" && addr ? addr.port : 0);
    });
  });
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.close(() => resolve());
  });
}

describe("KaptantoStream", () => {
  let server: Server;
  let port: number;

  afterEach(async () => {
    if (server) {
      server.closeAllConnections?.();
      await closeServer(server);
    }
  });

  it("yields events in order", async () => {
    const ev1 = makeEvent({ id: "ev-1", table: "orders" });
    const ev2 = makeEvent({ id: "ev-2", table: "users", operation: "update", before: { id: 2 } });
    const ev3 = makeEvent({ id: "ev-3", operation: "delete", before: { id: 3 }, after: null });

    server = createServer((_req, res) => {
      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      });
      res.write(sseFrame(ev1));
      res.write(sseFrame(ev2));
      res.write(sseFrame(ev3));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "test-svc",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 3) stream.close();
    }

    expect(received.map((e) => e.id)).toEqual(["ev-1", "ev-2", "ev-3"]);
  });

  it("yields ai_context from SSE wire format", async () => {
    const enriched = makeEvent({
      id: "ev-ai",
      ai_context: {
        intent: "fulfill_order",
        custom: { priority: "high" },
      },
    });

    server = createServer((_req, res) => {
      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      });
      res.write(sseFrame(enriched));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "test-svc",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].ai_context).toEqual(enriched.ai_context);
  });

  it("passes consumer, tables, and operations as query params", async () => {
    let requestUrl = "";
    server = createServer((req, res) => {
      requestUrl = req.url ?? "";
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(sseFrame(makeEvent()));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "my-svc",
      tables: ["orders", "users"],
      operations: ["insert", "update"],
    });

    for await (const _ of stream) {
      stream.close();
    }

    const params = new URL(requestUrl, `http://127.0.0.1:${port}`).searchParams;
    expect(params.get("consumer")).toBe("my-svc");
    expect(params.get("tables")).toBe("orders,users");
    expect(params.get("operations")).toBe("insert,update");
  });

  it("sends Authorization header when token is set", async () => {
    let authHeader = "";
    server = createServer((req, res) => {
      authHeader = req.headers.authorization ?? "";
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(sseFrame(makeEvent()));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
      token: "secret-token-123",
    });

    for await (const _ of stream) {
      stream.close();
    }

    expect(authHeader).toBe("Bearer secret-token-123");
  });

  it("ignores ping comments", async () => {
    const ev = makeEvent({ id: "after-ping" });
    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": ping\n\n");
      res.write(": keep-alive\n\n");
      res.write(sseFrame(ev));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 1) stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].id).toBe("after-ping");
  });

  it("skips malformed SSE lines with warning", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const ev = makeEvent({ id: "good" });

    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write("garbage-no-colon\n\n");
      res.write(sseFrame(ev));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 1) stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].id).toBe("good");
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("skipping malformed SSE line"),
      expect.anything(),
    );
    warnSpy.mockRestore();
  });

  it("skips non-JSON data with warning", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const ev = makeEvent({ id: "valid" });

    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write("data: not-valid-json\n\n");
      res.write(sseFrame(ev));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 1) stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].id).toBe("valid");
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("skipping non-JSON data"),
      expect.anything(),
    );
    warnSpy.mockRestore();
  });

  it("reconnects with backoff on disconnect mid-stream", async () => {
    let connectionCount = 0;

    server = createServer((_req, res) => {
      connectionCount++;
      res.writeHead(200, { "Content-Type": "text/event-stream" });

      if (connectionCount === 1) {
        res.destroy();
        return;
      }

      res.write(sseFrame(makeEvent({ id: "after-reconnect" })));
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 1) stream.close();
    }

    expect(connectionCount).toBeGreaterThanOrEqual(2);
    expect(received[0].id).toBe("after-reconnect");
  });

  it("close() terminates iteration promptly", async () => {
    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      const interval = setInterval(() => {
        res.write(": ping\n\n");
      }, 50);
      res.on("close", () => clearInterval(interval));
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const start = Date.now();
    setTimeout(() => stream.close(), 100);

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
    }

    const elapsed = Date.now() - start;
    expect(received).toHaveLength(0);
    expect(elapsed).toBeLessThan(3000);
  });

  it("ignores event: and id: SSE fields", async () => {
    const ev = makeEvent({ id: "with-event-field" });

    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(`event: change\nid: 42\ndata: ${JSON.stringify(ev)}\n\n`);
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      if (received.length === 1) stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].id).toBe("with-event-field");
  });

  it("normalizes CRLF line endings", async () => {
    const ev = makeEvent({ id: "crlf-event" });

    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(`data: ${JSON.stringify(ev)}\r\n\r\n`);
      res.end();
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const received: ChangeEvent[] = [];
    for await (const ev of stream) {
      received.push(ev);
      stream.close();
    }

    expect(received).toHaveLength(1);
    expect(received[0].id).toBe("crlf-event");
  });

  it("does not retry terminal 4xx responses", async () => {
    let connectionCount = 0;

    server = createServer((_req, res) => {
      connectionCount++;
      res.writeHead(401, { "Content-Type": "text/plain" });
      res.end("unauthorized");
    });
    port = await listen(server);

    const stream = new KaptantoStream({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "c",
    });

    const iterator = stream[Symbol.asyncIterator]();
    const result = await iterator.next();

    expect(result.done).toBe(true);
    expect(connectionCount).toBe(1);
  });

  it(
    "applies exponential backoff when body parsing fails repeatedly",
    async () => {
      let connectionCount = 0;

      server = createServer((_req, res) => {
        connectionCount++;
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        // Every response is valid HTTP but contains no parseable event, so the
        // stream stays "unhealthy" and the attempt counter must keep growing.
        res.write("data: not-valid-json\n\n");
        res.end();
      });
      port = await listen(server);

      const stream = new KaptantoStream({
        url: `http://127.0.0.1:${port}/events`,
        consumer: "c",
      });

      const start = Date.now();
      const iterator = stream[Symbol.asyncIterator]();
      const resultPromise = iterator.next();

      // Wait for the first connection, then for a retry so we prove backoff
      // is actually applied (BASE_DELAY_MS is 1000ms with jitter).
      await vi.waitFor(() => expect(connectionCount).toBeGreaterThanOrEqual(1), {
        timeout: 2000,
      });
      await vi.waitFor(() => expect(connectionCount).toBeGreaterThanOrEqual(2), {
        timeout: 5000,
      });
      stream.close();
      await resultPromise;

      const elapsed = Date.now() - start;
      expect(elapsed).toBeGreaterThanOrEqual(100);
    },
    10_000,
  );
});
