import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";

import { consumeKaptantoSse } from "./cdc-sse.ts";
import type { JsonObject, KaptantoEvent } from "./cdc-types.ts";

type Product = {
  id: string;
  name: string;
  category: string;
  priceCents: number;
  stockQuantity: number;
  description: string;
};

type SearchEntry = { id: string; name: string; category: string; terms: string[] };
type CacheEntry = {
  id: string;
  name: string;
  status: "hot" | "warming";
  version: number;
  refreshedAt: string;
};
type CacheMutation = { productId: string; name: string; action: string; ts: string };

const sourceView = {
  products: new Map<string, Product>(),
  eventsProcessed: 0,
};

const search = {
  index: new Map<string, SearchEntry>(),
  eventsProcessed: 0,
  lastIndexedAt: null as string | null,
};

const cache = {
  entries: new Map<string, CacheEntry>(),
  eventsProcessed: 0,
  refreshes: 0,
  invalidations: 0,
  recent: [] as CacheMutation[],
};

const clients = new Set<ServerResponse>();
const port = Number(process.env.PORT ?? "4006");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });

function snapshot() {
  return {
    source: {
      products: [...sourceView.products.values()],
      eventsProcessed: sourceView.eventsProcessed,
    },
    search: {
      index: [...search.index.values()],
      eventsProcessed: search.eventsProcessed,
      lastIndexedAt: search.lastIndexedAt,
    },
    cache: {
      entries: [...cache.entries.values()],
      eventsProcessed: cache.eventsProcessed,
      refreshes: cache.refreshes,
      invalidations: cache.invalidations,
      recent: cache.recent.slice(0, 12),
    },
  };
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

function pushCacheMutation(productId: string, name: string, action: string): void {
  cache.recent = [{ productId, name, action, ts: new Date().toISOString() }, ...cache.recent].slice(0, 24);
}

function applySource(event: KaptantoEvent): void {
  sourceView.eventsProcessed++;
  if (event.operation === "delete" && event.before) {
    sourceView.products.delete(String(event.before.id));
    return;
  }
  if (!event.after) return;
  const a = event.after;
  const id = String(a.id);
  const qty = Number(a.stock_quantity);
  sourceView.products.set(id, {
    id,
    name: String(a.name),
    category: String(a.category),
    priceCents: Number(a.price_cents),
    stockQuantity: qty,
    description: String(a.description ?? ""),
  });
}

function applySearch(event: KaptantoEvent): void {
  search.eventsProcessed++;
  if (event.operation === "delete" && event.before) {
    search.index.delete(String(event.before.id));
    return;
  }
  if (!event.after) return;
  const a = event.after;
  const id = String(a.id);
  const name = String(a.name);
  const category = String(a.category);
  const desc = String(a.description ?? "");
  const terms = [...new Set([
    ...name.toLowerCase().split(/\s+/),
    category.toLowerCase(),
    ...desc.toLowerCase().split(/\s+/).filter((w) => w.length > 3),
  ])];
  search.index.set(id, { id, name, category, terms });
  search.lastIndexedAt = new Date().toISOString();
}

function applyCache(event: KaptantoEvent): void {
  cache.eventsProcessed++;
  if (event.operation === "delete" && event.before) {
    const id = String(event.before.id);
    const name = String(event.before.name ?? id);
    cache.entries.delete(id);
    cache.invalidations++;
    pushCacheMutation(id, name, "evicted");
    return;
  }
  if (!event.after) return;
  const a = event.after;
  const id = String(a.id);
  const name = String(a.name);
  const existing = cache.entries.get(id);
  const version = (existing?.version ?? 0) + 1;
  const action = event.operation === "insert" ? "primed" : "refreshed";
  cache.entries.set(id, {
    id,
    name,
    status: "hot",
    version,
    refreshedAt: new Date().toISOString(),
  });
  if (event.operation === "update") {
    cache.invalidations++;
  }
  cache.refreshes++;
  pushCacheMutation(id, name, action);
}

async function hydrateProducts(): Promise<void> {
  const result = await pool.query<{
    id: string; name: string; category: string;
    price_cents: number; stock_quantity: number; description: string | null;
  }>("SELECT id, name, category, price_cents, stock_quantity, description FROM products ORDER BY created_at");

  for (const row of result.rows) {
    const id = row.id;
    const product: Product = {
      id,
      name: row.name,
      category: row.category,
      priceCents: row.price_cents,
      stockQuantity: row.stock_quantity,
      description: row.description ?? "",
    };
    sourceView.products.set(id, product);
    const terms = [...new Set([
      ...row.name.toLowerCase().split(/\s+/),
      row.category.toLowerCase(),
      ...(row.description ?? "").toLowerCase().split(/\s+/).filter((w) => w.length > 3),
    ])];
    search.index.set(id, { id, name: row.name, category: row.category, terms });
    cache.entries.set(id, {
      id,
      name: row.name,
      status: "hot",
      version: 1,
      refreshedAt: new Date().toISOString(),
    });
  }
}

async function hydrateProductsWithRetry(): Promise<void> {
  for (let attempt = 1; ; attempt++) {
    try {
      await hydrateProducts();
      return;
    } catch (err) {
      if (attempt >= 20) {
        throw err;
      }
      console.error(`fanout hydrate attempt ${attempt} failed`, err);
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
}

function startConsumer(consumerId: string, apply: (e: KaptantoEvent) => void): void {
  const base = process.env.KAPTANTO_BASE_URL;
  if (!base) throw new Error("KAPTANTO_BASE_URL is required");
  const url = `${base}/events?consumer=${consumerId}&tables=products`;

  async function loop(): Promise<void> {
    while (true) {
      try {
        await consumeKaptantoSse(url, (event) => { apply(event); broadcast(); });
      } catch (err) {
        console.error(`${consumerId} reconnecting`, err);
        await new Promise((r) => setTimeout(r, 1500));
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

  if (req.method === "POST" && req.url === "/api/products") {
    const body = await parseBody(req);
    const id = randomUUID();
    await pool.query(
      "INSERT INTO products (id, name, category, price_cents, stock_quantity, description) VALUES ($1, $2, $3, $4, $5, $6)",
      [id, String(body.name ?? "New product"), String(body.category ?? "General"),
       Number(body.priceCents ?? 9900), Number(body.stockQuantity ?? 10), String(body.description ?? "")],
    );
    sendJson(res, 201, { ok: true, id });
    return;
  }

  const patchMatch = req.url.match(/^\/api\/products\/([^/]+)$/);
  if (req.method === "PATCH" && patchMatch) {
    const body = await parseBody(req);
    const fields: string[] = [];
    const values: unknown[] = [];
    if (body.name !== undefined) { fields.push(`name = $${fields.length + 1}`); values.push(String(body.name)); }
    if (body.category !== undefined) { fields.push(`category = $${fields.length + 1}`); values.push(String(body.category)); }
    if (body.description !== undefined) { fields.push(`description = $${fields.length + 1}`); values.push(String(body.description)); }
    if (body.priceCents !== undefined) { fields.push(`price_cents = $${fields.length + 1}`); values.push(Number(body.priceCents)); }
    if (body.stockQuantity !== undefined) { fields.push(`stock_quantity = $${fields.length + 1}`); values.push(Number(body.stockQuantity)); }
    if (fields.length === 0) { sendJson(res, 400, { error: "no fields" }); return; }
    values.push(patchMatch[1]);
    await pool.query(`UPDATE products SET ${fields.join(", ")} WHERE id = $${values.length}`, values);
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
}).listen(port, () => console.log(`fanout api listening on ${port}`));

hydrateProductsWithRetry()
  .then(() => {
    startConsumer("source-view", applySource);
    startConsumer("search-indexer", applySearch);
    startConsumer("cache-service", applyCache);
  })
  .catch((err) => { console.error(err); process.exit(1); });
