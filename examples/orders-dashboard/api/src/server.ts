import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";

import { consumeKaptantoSse } from "../../../shared/src/cdc-sse.js";
import type { JsonObject, KaptantoEvent } from "../../../shared/src/cdc-types.js";

type OrderView = {
  id: string;
  customerName: string;
  totalCents: number;
  orderStatus: string;
  paymentStatus: string;
  shipmentStatus: string;
};

const port = Number(process.env.PORT ?? "4002");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const orders = new Map<string, OrderView>();
let recentEvents: string[] = [];
const clients = new Set<ServerResponse>();

function snapshot() {
  return {
    orders: [...orders.values()],
    recentEvents,
  };
}

function broadcast(): void {
  const payload = `data: ${JSON.stringify(snapshot())}\n\n`;
  for (const client of clients) {
    client.write(payload);
  }
}

async function parseBody(req: IncomingMessage): Promise<JsonObject> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.from(chunk));
  }
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
  recentEvents = [message, ...recentEvents].slice(0, 12);
}

function applyEvent(event: KaptantoEvent): void {
  if (!event.after) return;
  const after = event.after;
  if (event.table === "orders") {
    const id = String(after.id);
    orders.set(id, {
      id,
      customerName: String(after.customer_name),
      totalCents: Number(after.total_cents),
      orderStatus: String(after.status),
      paymentStatus: orders.get(id)?.paymentStatus ?? "pending",
      shipmentStatus: orders.get(id)?.shipmentStatus ?? "not_created",
    });
    note(`Order ${id} created for ${String(after.customer_name)}`);
  }
  if (event.table === "payments") {
    const id = String(after.order_id);
    const current = orders.get(id);
    if (current) {
      current.paymentStatus = String(after.status);
      orders.set(id, current);
      note(`Payment ${String(after.status)} for ${id}`);
    }
  }
  if (event.table === "shipments") {
    const id = String(after.order_id);
    const current = orders.get(id);
    if (current) {
      current.shipmentStatus = String(after.status);
      current.orderStatus =
        current.shipmentStatus === "delivered"
          ? "completed"
          : current.shipmentStatus === "packed"
            ? "processing"
            : current.orderStatus;
      orders.set(id, current);
      note(`Shipment ${String(after.status)} for ${id}`);
    }
  }
}

async function hydrateOrders(): Promise<void> {
  const result = await pool.query<{
    id: string;
    customer_name: string;
    total_cents: number;
    status: string;
  }>("SELECT id, customer_name, total_cents, status FROM orders ORDER BY created_at DESC");

  for (const row of result.rows) {
    orders.set(row.id, {
      id: row.id,
      customerName: row.customer_name,
      totalCents: row.total_cents,
      orderStatus: row.status,
      paymentStatus: "pending",
      shipmentStatus: "not_created",
    });
  }
}

async function createOrder(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = randomUUID();
  await pool.query(
    "INSERT INTO orders (id, customer_name, total_cents, status) VALUES ($1, $2, $3, $4)",
    [id, String(body.customerName ?? "Acme Retail"), Number(body.totalCents ?? 12900), "created"],
  );
  sendJson(res, 201, { ok: true, id });
}

async function createPayment(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = randomUUID();
  await pool.query(
    "INSERT INTO payments (id, order_id, status, amount_cents) VALUES ($1, $2, $3, $4)",
    [id, String(body.orderId), String(body.status ?? "captured"), Number(body.amountCents ?? 12900)],
  );
  sendJson(res, 201, { ok: true, id });
}

async function createShipment(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = randomUUID();
  await pool.query(
    "INSERT INTO shipments (id, order_id, carrier, status) VALUES ($1, $2, $3, $4)",
    [id, String(body.orderId), String(body.carrier ?? "DHL"), String(body.status ?? "packed")],
  );
  sendJson(res, 201, { ok: true, id });
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
    clients.add(res);
    res.write(`data: ${JSON.stringify(snapshot())}\n\n`);
    req.on("close", () => clients.delete(res));
    return;
  }

  if (req.method === "POST" && req.url === "/api/orders") {
    await createOrder(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/payments") {
    await createPayment(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/shipments") {
    await createShipment(await parseBody(req), res);
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
      console.error("orders consumer reconnecting", error);
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
}

createServer((req, res) => {
  requestHandler(req, res).catch((error: unknown) => {
    console.error(error);
    sendJson(res, 500, { error: "internal_error" });
  });
}).listen(port, () => {
  console.log(`orders api listening on ${port}`);
});

hydrateOrders()
  .then(() => startConsumer())
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
