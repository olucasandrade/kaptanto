import { randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { Pool } from "pg";

type JsonObject = Record<string, unknown>;

const port = Number(process.env.PORT ?? "4010");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });

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

async function createSubscription(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = String(body.id ?? randomUUID());
  await pool.query(
    "INSERT INTO subscriptions (id, customer_id, plan, status) VALUES ($1, $2, $3, $4)",
    [id, String(body.customerId ?? "acme"), String(body.plan ?? "pro"), String(body.status ?? "trialing")],
  );
  sendJson(res, 201, { ok: true, id });
}

async function updateSubscription(body: JsonObject, res: ServerResponse): Promise<void> {
  await pool.query(
    "UPDATE subscriptions SET status = $2, updated_at = NOW() WHERE id = $1",
    [String(body.subscriptionId), String(body.status ?? "active")],
  );
  sendJson(res, 200, { ok: true });
}

async function createPayment(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = String(body.id ?? randomUUID());
  await pool.query(
    "INSERT INTO invoice_payments (id, subscription_id, customer_id, status, amount_cents) VALUES ($1, $2, $3, $4, $5)",
    [
      id,
      String(body.subscriptionId ?? "sub_seed_acme"),
      String(body.customerId ?? "acme"),
      String(body.status ?? "paid"),
      Number(body.amountCents ?? 4900),
    ],
  );
  sendJson(res, 201, { ok: true, id });
}

async function listState(res: ServerResponse): Promise<void> {
  const subscriptions = await pool.query(
    "SELECT id, customer_id, plan, status, updated_at FROM subscriptions ORDER BY updated_at DESC",
  );
  const payments = await pool.query(
    "SELECT id, subscription_id, customer_id, status, amount_cents, created_at FROM invoice_payments ORDER BY created_at DESC",
  );
  sendJson(res, 200, {
    subscriptions: subscriptions.rows,
    payments: payments.rows,
  });
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

  if (req.method === "GET" && req.url === "/api/state") {
    await listState(res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/subscriptions") {
    await createSubscription(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/subscriptions/status") {
    await updateSubscription(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/payments") {
    await createPayment(await parseBody(req), res);
    return;
  }

  sendJson(res, 404, { error: "Not found" });
}

createServer((req, res) => {
  requestHandler(req, res).catch((error: unknown) => {
    console.error(error);
    sendJson(res, 500, { error: "internal_error" });
  });
}).listen(port, () => {
  console.log(`billing api listening on ${port}`);
});

