import { createServer, type Server } from "node:http";
import { describe, it, expect, afterEach, vi } from "vitest";
import type { ChangeEvent } from "@kaptanto/events";
import {
  kaptantoTrigger,
  type MastraWorkflow,
  type MastraWorkflowRun,
} from "../src/trigger.js";
import { createKaptantoStream } from "../src/stream.js";
import { toAgentContext } from "../src/context.js";

function makeEvent(overrides: Partial<ChangeEvent> = {}): ChangeEvent {
  return {
    id: "01J000000000000000000000AA",
    idempotency_key: "pg:0/1234:1",
    timestamp: "2025-01-01T00:00:00Z",
    source: "postgres",
    operation: "update",
    schema: "public",
    table: "orders",
    key: { id: 1 },
    before: { id: 1, status: "pending" },
    after: { id: 1, status: "shipped" },
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

/**
 * Mock Mastra workflow that pins the createRun → start({ inputData }) API
 * used by Mastra as of @mastra/core 1.x / mastra 1.x.
 */
function mockWorkflow() {
  const starts: Array<{ runId?: string; inputData: unknown }> = [];
  const createRun = vi.fn(
    async (opts?: { runId?: string; resourceId?: string }) => {
      const run: MastraWorkflowRun = {
        start: vi.fn(async ({ inputData }) => {
          starts.push({ runId: opts?.runId, inputData });
          return { status: "success", result: { ok: true } };
        }),
      };
      return run;
    },
  );
  const workflow: MastraWorkflow = { createRun };
  return { workflow, createRun, starts };
}

/** SSE server that writes events once and keeps the connection open (no reconnect loop). */
function openSseServer(events: ChangeEvent[]): Promise<{ server: Server; port: number }> {
  const server = createServer((_req, res) => {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
    });
    for (const ev of events) {
      res.write(sseFrame(ev));
    }
    // Keep the socket open so KaptantoStream does not reconnect and re-deliver.
  });
  return listen(server).then((port) => ({ server, port }));
}

describe("createKaptantoStream", () => {
  it("returns a KaptantoStream instance", () => {
    const stream = createKaptantoStream({
      url: "http://127.0.0.1:9/events",
      consumer: "test",
    });
    expect(typeof stream[Symbol.asyncIterator]).toBe("function");
    expect(typeof stream.close).toBe("function");
    stream.close();
  });
});

describe("kaptantoTrigger (integration vs mock SSE)", () => {
  let server: Server;

  afterEach(async () => {
    if (server) {
      server.closeAllConnections?.();
      await closeServer(server);
    }
  });

  it("starts a Mastra workflow run per SSE event via createRun().start({ inputData })", async () => {
    const ev1 = makeEvent({ id: "ev-1", idempotency_key: "key-1" });
    const ev2 = makeEvent({
      id: "ev-2",
      idempotency_key: "key-2",
      after: { id: 1, status: "delivered" },
    });

    const opened = await openSseServer([ev1, ev2]);
    server = opened.server;
    const port = opened.port;

    const { workflow, createRun, starts } = mockWorkflow();

    const handle = kaptantoTrigger({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "mastra-test",
      tables: ["public.orders"],
      workflow,
    });

    await vi.waitFor(() => {
      expect(starts).toHaveLength(2);
    });
    handle.close();
    await handle.done;

    // Pin Mastra trigger API: createRun then start({ inputData })
    expect(createRun).toHaveBeenCalledTimes(2);
    expect(createRun).toHaveBeenNthCalledWith(1, { runId: "key-1" });
    expect(createRun).toHaveBeenNthCalledWith(2, { runId: "key-2" });

    expect(starts[0].inputData).toEqual({
      context: toAgentContext(ev1),
      event: ev1,
    });
    expect(starts[1].inputData).toEqual({
      context: toAgentContext(ev2),
      event: ev2,
    });

    // Each createRun returned a run whose start was invoked
    for (const call of createRun.mock.results) {
      const run = await call.value;
      expect(run.start).toHaveBeenCalledOnce();
      expect(run.start.mock.calls[0][0]).toHaveProperty("inputData");
    }
  });

  it("honors mapEvent for custom inputData", async () => {
    const ev = makeEvent({ id: "mapped" });
    const opened = await openSseServer([ev]);
    server = opened.server;

    const { workflow, starts } = mockWorkflow();
    const handle = kaptantoTrigger({
      url: `http://127.0.0.1:${opened.port}/events`,
      consumer: "mastra-test",
      workflow,
      mapEvent: (e) => ({ orderId: (e.key as { id: number }).id, op: e.operation }),
    });

    await vi.waitFor(() => {
      expect(starts).toHaveLength(1);
    });
    handle.close();
    await handle.done;

    expect(starts[0].inputData).toEqual({ orderId: 1, op: "update" });
  });

  it("continues after workflow errors and reports via onError", async () => {
    const ev1 = makeEvent({ id: "fail", idempotency_key: "k-fail" });
    const ev2 = makeEvent({ id: "ok", idempotency_key: "k-ok" });

    const opened = await openSseServer([ev1, ev2]);
    server = opened.server;

    const errors: Array<{ err: unknown; id: string }> = [];
    let call = 0;
    const workflow: MastraWorkflow = {
      createRun: async () => ({
        start: async () => {
          call++;
          if (call === 1) throw new Error("boom");
          return { status: "success" };
        },
      }),
    };

    const handle = kaptantoTrigger({
      url: `http://127.0.0.1:${opened.port}/events`,
      consumer: "mastra-test",
      workflow,
      onError: (err, e) => errors.push({ err, id: e.id }),
    });

    await vi.waitFor(() => {
      expect(call).toBe(2);
    });
    handle.close();
    await handle.done;

    expect(errors).toHaveLength(1);
    expect(errors[0].id).toBe("fail");
    expect((errors[0].err as Error).message).toBe("boom");
  });

  it("stops when AbortSignal fires", async () => {
    server = createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      const interval = setInterval(() => {
        res.write(": ping\n\n");
      }, 50);
      res.on("close", () => clearInterval(interval));
    });
    const port = await listen(server);

    const { workflow, starts } = mockWorkflow();
    const ac = new AbortController();
    const handle = kaptantoTrigger({
      url: `http://127.0.0.1:${port}/events`,
      consumer: "mastra-test",
      workflow,
      signal: ac.signal,
    });

    await new Promise((r) => setTimeout(r, 100));
    ac.abort();
    await handle.done;

    expect(starts).toHaveLength(0);
  });
});
