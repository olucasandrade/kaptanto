import {
  createServer,
  type Server,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import { describe, it, expect, afterEach, vi, type Mock } from "vitest";
import { KaptantoTrigger } from "../nodes/KaptantoTrigger/KaptantoTrigger.node.js";
import type { ChangeEvent } from "@kaptanto/events";

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

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

interface MockTriggerContext {
  emittedItems: Record<string, unknown>[][];
  emit: Mock;
  getCredentials: Mock;
  getNodeParameter: Mock;
  helpers: { returnJsonArray: (items: Record<string, unknown>[]) => Record<string, unknown>[] };
}

function createMockContext(
  port: number,
  overrides: {
    tables?: string;
    operations?: string[];
    consumerId?: string;
    authToken?: string;
  } = {},
): MockTriggerContext {
  const emittedItems: Record<string, unknown>[][] = [];

  const ctx: MockTriggerContext = {
    emittedItems,
    emit: vi.fn((data: Record<string, unknown>[][]) => {
      emittedItems.push(...data);
    }),
    getCredentials: vi.fn().mockResolvedValue({
      baseUrl: `http://127.0.0.1:${port}`,
      authToken: overrides.authToken ?? "",
    }),
    getNodeParameter: vi.fn((name: string, fallback: unknown) => {
      const params: Record<string, unknown> = {
        tables: overrides.tables ?? "",
        operations: overrides.operations ?? [],
        consumerId: overrides.consumerId ?? "test-workflow-id",
      };
      return params[name] ?? fallback;
    }),
    helpers: {
      returnJsonArray: (items: Record<string, unknown>[]) => items,
    },
  };

  return ctx;
}

describe("KaptantoTrigger", () => {
  let server: Server;

  afterEach(async () => {
    if (server) {
      server.closeAllConnections?.();
      await closeServer(server);
    }
  });

  describe("node description", () => {
    it("has required n8n metadata", () => {
      const node = new KaptantoTrigger();
      const desc = node.description;

      expect(desc.name).toBe("kaptantoTrigger");
      expect(desc.group).toContain("trigger");
      expect(desc.credentials).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ name: "kaptantoApi" }),
        ]),
      );
      expect(desc.inputs).toEqual([]);
      expect(desc.outputs).toEqual(["main"]);
    });

    it("defines tables, operations, and consumerId properties", () => {
      const node = new KaptantoTrigger();
      const propNames = node.description.properties.map((p) => p.name);
      expect(propNames).toContain("tables");
      expect(propNames).toContain("operations");
      expect(propNames).toContain("consumerId");
    });
  });

  describe("trigger()", () => {
    it("emits one item per SSE event", async () => {
      const ev1 = makeEvent({ id: "ev-1" });
      const ev2 = makeEvent({ id: "ev-2", operation: "update", before: { id: 2 } });

      server = createServer((_req: IncomingMessage, res: ServerResponse) => {
        res.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        });
        res.write(sseFrame(ev1));
        res.write(sseFrame(ev2));
        setTimeout(() => res.end(), 50);
      });
      const port = await listen(server);

      const ctx = createMockContext(port);
      const node = new KaptantoTrigger();
      const { closeFunction } = await node.trigger.call(ctx as any);

      await wait(500);
      await closeFunction!();

      expect(ctx.emittedItems.length).toBeGreaterThanOrEqual(2);
      const ids = ctx.emittedItems.flat().map((item) => (item as any).id);
      expect(ids).toContain("ev-1");
      expect(ids).toContain("ev-2");
    });

    it("reconnects after SSE stream drops", async () => {
      let connectionCount = 0;

      server = createServer((_req: IncomingMessage, res: ServerResponse) => {
        connectionCount++;
        res.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
        });

        if (connectionCount === 1) {
          res.destroy();
          return;
        }

        res.write(sseFrame(makeEvent({ id: "after-reconnect" })));
        setTimeout(() => res.end(), 50);
      });
      const port = await listen(server);

      const ctx = createMockContext(port);
      const node = new KaptantoTrigger();
      const { closeFunction } = await node.trigger.call(ctx as any);

      await wait(4000);
      await closeFunction!();

      expect(connectionCount).toBeGreaterThanOrEqual(2);
      const ids = ctx.emittedItems.flat().map((item) => (item as any).id);
      expect(ids).toContain("after-reconnect");
    }, 10_000);

    it("closeFunction stops cleanly", async () => {
      server = createServer((_req: IncomingMessage, res: ServerResponse) => {
        res.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
        });
        const interval = setInterval(() => {
          res.write(sseFrame(makeEvent({ id: `tick-${Date.now()}` })));
        }, 100);
        res.on("close", () => clearInterval(interval));
      });
      const port = await listen(server);

      const ctx = createMockContext(port);
      const node = new KaptantoTrigger();
      const { closeFunction } = await node.trigger.call(ctx as any);

      await wait(300);
      const countBefore = ctx.emittedItems.length;
      await closeFunction!();
      await wait(300);
      const countAfter = ctx.emittedItems.length;

      expect(countAfter - countBefore).toBeLessThanOrEqual(1);
    });

    it("passes tables and operations as query params", async () => {
      let requestUrl = "";

      server = createServer((req: IncomingMessage, res: ServerResponse) => {
        requestUrl = req.url ?? "";
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        res.write(sseFrame(makeEvent()));
        setTimeout(() => res.end(), 50);
      });
      const port = await listen(server);

      const ctx = createMockContext(port, {
        tables: "public.orders, public.users",
        operations: ["insert", "update"],
        consumerId: "my-workflow",
      });
      const node = new KaptantoTrigger();
      const { closeFunction } = await node.trigger.call(ctx as any);

      await wait(500);
      await closeFunction!();

      const params = new URL(requestUrl, `http://127.0.0.1:${port}`).searchParams;
      expect(params.get("consumer")).toBe("my-workflow");
      expect(params.get("tables")).toBe("public.orders,public.users");
      expect(params.get("operations")).toBe("insert,update");
    });

    it("sends auth token when configured", async () => {
      let authHeader = "";

      server = createServer((req: IncomingMessage, res: ServerResponse) => {
        authHeader = req.headers.authorization ?? "";
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        res.write(sseFrame(makeEvent()));
        setTimeout(() => res.end(), 50);
      });
      const port = await listen(server);

      const ctx = createMockContext(port, { authToken: "my-secret-token" });
      const node = new KaptantoTrigger();
      const { closeFunction } = await node.trigger.call(ctx as any);

      await wait(500);
      await closeFunction!();

      expect(authHeader).toBe("Bearer my-secret-token");
    });
  });
});
