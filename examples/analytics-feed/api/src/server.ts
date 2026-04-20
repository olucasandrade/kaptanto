import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { MongoClient } from "mongodb";

import { consumeKaptantoSse } from "../../../shared/src/cdc-sse.js";
import type { JsonObject, KaptantoEvent } from "../../../shared/src/cdc-types.js";

type EventRow = {
  id: string;
  type: string;
  actor: string;
  workspace: string;
  createdAt: string;
};

const port = Number(process.env.PORT ?? "4003");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const mongoUrl = process.env.MONGODB_URL ?? "mongodb://localhost:27017";
const databaseName = process.env.DATABASE_NAME ?? "analytics_demo";
const client = new MongoClient(mongoUrl);
const collection = client.db(databaseName).collection("product_events");
const feed: EventRow[] = [];
const countsByType: Record<string, number> = {};
const countsByWorkspace: Record<string, number> = {};
const sseClients = new Set<ServerResponse>();

function snapshot() {
  return {
    feed,
    countsByType,
    countsByWorkspace,
  };
}

function broadcast(): void {
  const payload = `data: ${JSON.stringify(snapshot())}\n\n`;
  for (const client of sseClients) {
    client.write(payload);
  }
}

function sendJson(res: ServerResponse, status: number, payload: unknown): void {
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Access-Control-Allow-Origin": allowedOrigin,
  });
  res.end(JSON.stringify(payload));
}

async function parseBody(req: IncomingMessage): Promise<JsonObject> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.from(chunk));
  }
  return chunks.length ? (JSON.parse(Buffer.concat(chunks).toString("utf8")) as JsonObject) : {};
}

function applyEvent(event: KaptantoEvent): void {
  const after = event.after;
  if (!after) {
    return;
  }

  const type = String(after.type ?? "unknown");
  const workspace = String(after.workspace ?? "default");
  const row: EventRow = {
    id: String(after._id ?? event.id),
    type,
    actor: String(after.actor ?? "system"),
    workspace,
    createdAt: String(after.createdAt ?? new Date().toISOString()),
  };

  feed.unshift(row);
  if (feed.length > 20) {
    feed.pop();
  }
  countsByType[type] = (countsByType[type] ?? 0) + 1;
  countsByWorkspace[workspace] = (countsByWorkspace[workspace] ?? 0) + 1;
}

async function createEvent(body: JsonObject, res: ServerResponse): Promise<void> {
  const doc = {
    type: String(body.type ?? "comment.created"),
    actor: String(body.actor ?? "ava"),
    workspace: String(body.workspace ?? "northwind"),
    createdAt: new Date(),
    metadata: (body.metadata ?? {}) as Record<string, unknown>,
  };
  const result = await collection.insertOne(doc);
  sendJson(res, 201, { ok: true, id: String(result.insertedId) });
}

async function hydrateProjection(): Promise<void> {
  const docs = await collection.find({}, { sort: { createdAt: -1 }, limit: 20 }).toArray();
  docs.reverse().forEach((doc) => {
    applyEvent({
      id: String(doc._id),
      table: "product_events",
      operation: "read",
      after: {
        _id: String(doc._id),
        type: doc.type,
        actor: doc.actor,
        workspace: doc.workspace,
        createdAt: doc.createdAt instanceof Date ? doc.createdAt.toISOString() : String(doc.createdAt),
      },
    });
  });
}

async function seedIfEmpty(): Promise<void> {
  const total = await collection.countDocuments();
  if (total > 0) {
    return;
  }

  await collection.insertMany([
    {
      type: "workspace.created",
      actor: "ava",
      workspace: "northwind",
      createdAt: new Date(),
      metadata: { source: "seed" },
    },
    {
      type: "report.exported",
      actor: "jules",
      workspace: "northwind",
      createdAt: new Date(),
      metadata: { source: "seed" },
    },
  ]);
}

async function requestHandler(req: IncomingMessage, res: ServerResponse): Promise<void> {
  if (!req.url) {
    sendJson(res, 404, { error: "Not found" });
    return;
  }

  if (req.method === "OPTIONS") {
    res.writeHead(204, {
      "Access-Control-Allow-Origin": allowedOrigin,
      "Access-Control-Allow-Methods": "GET,POST,OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type",
    });
    res.end();
    return;
  }

  if (req.method === "GET" && req.url === "/healthz") {
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "GET" && req.url === "/api/bootstrap") {
    sendJson(res, 200, snapshot());
    return;
  }

  if (req.method === "GET" && req.url === "/api/events") {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "Access-Control-Allow-Origin": allowedOrigin,
    });
    sseClients.add(res);
    res.write(`data: ${JSON.stringify(snapshot())}\n\n`);
    req.on("close", () => sseClients.delete(res));
    return;
  }

  if (req.method === "POST" && req.url === "/api/events") {
    await createEvent(await parseBody(req), res);
    return;
  }

  sendJson(res, 404, { error: "Not found" });
}

async function startConsumer(): Promise<void> {
  const url = process.env.KAPTANTO_URL;
  if (!url) throw new Error("KAPTANTO_URL is required");

  while (true) {
    try {
      await consumeKaptantoSse(url, (event) => {
        applyEvent(event);
        broadcast();
      });
    } catch (error) {
      console.error("analytics consumer reconnecting", error);
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
}

async function main(): Promise<void> {
  await client.connect();
  await seedIfEmpty();
  await hydrateProjection();
  createServer((req, res) => {
    requestHandler(req, res).catch((error: unknown) => {
      console.error(error);
      sendJson(res, 500, { error: "internal_error" });
    });
  }).listen(port, () => {
    console.log(`analytics api listening on ${port}`);
  });

  await startConsumer();
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
