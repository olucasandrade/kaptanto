import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";

import { consumeKaptantoSse } from "../../../shared/src/cdc-sse.ts";
import type { JsonObject, KaptantoEvent } from "../../../shared/src/cdc-types.ts";

type Job = {
  id: string;
  title: string;
  status: string;
  priority: string;
};

type ConsumerMeta = {
  status: "connected" | "paused";
  eventsProcessed: number;
  lastEventId: string | null;
  pausedAt: string | null;
};

const port = Number(process.env.PORT ?? "4005");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });

const jobs = new Map<string, Job>();
let recentEvents: string[] = [];
const clients = new Set<ServerResponse>();

const meta: ConsumerMeta = {
  status: "connected",
  eventsProcessed: 0,
  lastEventId: null,
  pausedAt: null,
};

let abortController: AbortController | null = null;

function snapshot() {
  return { jobs: [...jobs.values()], recentEvents, consumer: meta };
}

function broadcast(): void {
  const payload = `data: ${JSON.stringify(snapshot())}\n\n`;
  for (const client of clients) client.write(payload);
}

async function parseBody(req: IncomingMessage): Promise<JsonObject> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  return chunks.length ? (JSON.parse(Buffer.concat(chunks).toString("utf8")) as JsonObject) : {};
}

function sendJson(res: ServerResponse, status: number, payload: unknown): void {
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Access-Control-Allow-Origin": allowedOrigin,
  });
  res.end(JSON.stringify(payload));
}

function note(message: string): void {
  recentEvents = [message, ...recentEvents].slice(0, 20);
}

function applyEvent(event: KaptantoEvent): void {
  meta.eventsProcessed++;
  meta.lastEventId = event.id;

  if (event.operation === "delete" && event.before) {
    jobs.delete(String(event.before.id));
    note(`[delete] "${String(event.before.title)}" removed`);
    return;
  }
  if (!event.after) return;

  const after = event.after;
  const id = String(after.id);
  jobs.set(id, {
    id,
    title: String(after.title),
    status: String(after.status),
    priority: String(after.priority),
  });

  if (event.operation === "insert") {
    note(`[insert] "${String(after.title)}" queued`);
  } else if (event.operation === "update" && event.before) {
    const prev = event.before;
    if (prev.status !== after.status) {
      note(`[update] "${String(after.title)}" → ${String(after.status)}`);
    }
  }
}

async function hydrateJobs(): Promise<void> {
  const result = await pool.query<{ id: string; title: string; status: string; priority: string }>(
    "SELECT id, title, status, priority FROM jobs ORDER BY created_at DESC",
  );
  for (const row of result.rows) {
    jobs.set(row.id, { id: row.id, title: row.title, status: row.status, priority: row.priority });
  }
}

function startConsumerLoop(): void {
  const url = process.env.KAPTANTO_URL;
  if (!url) throw new Error("KAPTANTO_URL is required");

  async function loop(): Promise<void> {
    while (true) {
      if (meta.status === "paused") {
        await new Promise((r) => setTimeout(r, 400));
        continue;
      }
      abortController = new AbortController();
      try {
        await consumeKaptantoSse(
          url,
          (event) => { applyEvent(event); broadcast(); },
          { signal: abortController.signal },
        );
      } catch (err: unknown) {
        const isAbort = err instanceof Error && err.name === "AbortError";
        if (!isAbort) {
          console.error("consumer reconnecting", err);
          await new Promise((r) => setTimeout(r, 1500));
        }
      }
    }
  }

  loop().catch(console.error);
}

async function requestHandler(req: IncomingMessage, res: ServerResponse): Promise<void> {
  if (!req.url) { sendJson(res, 404, { error: "not_found" }); return; }

  if (req.method === "OPTIONS") {
    res.writeHead(204, {
      "Access-Control-Allow-Origin": allowedOrigin,
      "Access-Control-Allow-Methods": "GET,POST,PATCH,OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type",
    });
    res.end();
    return;
  }

  if (req.method === "GET" && req.url === "/healthz") { sendJson(res, 200, { ok: true }); return; }

  if (req.method === "GET" && req.url === "/api/bootstrap") { sendJson(res, 200, snapshot()); return; }

  if (req.method === "GET" && req.url === "/api/events") {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "Access-Control-Allow-Origin": allowedOrigin,
    });
    clients.add(res);
    res.write(`data: ${JSON.stringify(snapshot())}\n\n`);
    req.on("close", () => clients.delete(res));
    return;
  }

  if (req.method === "POST" && req.url === "/api/consumer/pause") {
    meta.status = "paused";
    meta.pausedAt = new Date().toISOString();
    abortController?.abort();
    note(`[system] consumer paused after ${meta.eventsProcessed} events`);
    broadcast();
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "POST" && req.url === "/api/consumer/resume") {
    meta.status = "connected";
    meta.pausedAt = null;
    note(`[system] consumer resumed — Kaptanto replays from cursor`);
    broadcast();
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "POST" && req.url === "/api/jobs") {
    const body = await parseBody(req);
    const id = randomUUID();
    await pool.query(
      "INSERT INTO jobs (id, title, status, priority) VALUES ($1, $2, $3, $4)",
      [id, String(body.title ?? "Untitled job"), "queued", String(body.priority ?? "normal")],
    );
    sendJson(res, 201, { ok: true, id });
    return;
  }

  const patchMatch = req.url.match(/^\/api\/jobs\/([^/]+)$/);
  if (req.method === "PATCH" && patchMatch) {
    const body = await parseBody(req);
    await pool.query("UPDATE jobs SET status = $1 WHERE id = $2", [
      String(body.status),
      patchMatch[1],
    ]);
    sendJson(res, 200, { ok: true });
    return;
  }

  sendJson(res, 404, { error: "not_found" });
}

createServer((req, res) => {
  requestHandler(req, res).catch((err: unknown) => {
    console.error(err);
    sendJson(res, 500, { error: "internal_error" });
  });
}).listen(port, () => console.log(`cursor-resume api listening on ${port}`));

hydrateJobs()
  .then(() => startConsumerLoop())
  .catch((err) => { console.error(err); process.exit(1); });
