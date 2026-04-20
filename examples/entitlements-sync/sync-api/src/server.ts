import { createServer, type IncomingMessage, type ServerResponse } from "node:http";

import { consumeKaptantoSse } from "../../../shared/src/cdc-sse.js";
import type { JsonObject, KaptantoEvent } from "../../../shared/src/cdc-types.js";

type SyncRecord = {
  sourceTable: string;
  customerId: string;
  action: string;
  createdAt: string;
};

const port = Number(process.env.PORT ?? "4012");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const recentSyncs: SyncRecord[] = [];

function sendJson(res: ServerResponse, status: number, payload: unknown): void {
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Access-Control-Allow-Origin": allowedOrigin,
  });
  res.end(JSON.stringify(payload));
}

function remember(record: SyncRecord): void {
  recentSyncs.unshift(record);
  if (recentSyncs.length > 20) {
    recentSyncs.pop();
  }
}

async function syncEntitlement(payload: JsonObject): Promise<void> {
  const url = process.env.ENTITLEMENTS_URL;
  if (!url) {
    throw new Error("ENTITLEMENTS_URL is required");
  }

  const response = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(payload),
  });

  if (!response.ok) {
    throw new Error(`entitlements api returned ${response.status}`);
  }
}

async function applyEvent(event: KaptantoEvent): Promise<void> {
  if (!event.after || event.operation === "control") {
    return;
  }

  const after = event.after;
  if (event.table === "subscriptions") {
    const status = String(after.status ?? "trialing");
    await syncEntitlement({
      customerId: String(after.customer_id),
      subscriptionId: String(after.id),
      plan: String(after.plan ?? "starter"),
      active: status === "active",
      reason: `subscription.${status}`,
    });
    remember({
      sourceTable: event.table,
      customerId: String(after.customer_id),
      action: `subscription.${status}`,
      createdAt: new Date().toISOString(),
    });
    return;
  }

  if (event.table === "invoice_payments") {
    const status = String(after.status ?? "pending");
    await syncEntitlement({
      customerId: String(after.customer_id),
      subscriptionId: String(after.subscription_id),
      plan: "billing-driven",
      active: status === "paid",
      reason: `payment.${status}`,
    });
    remember({
      sourceTable: event.table,
      customerId: String(after.customer_id),
      action: `payment.${status}`,
      createdAt: new Date().toISOString(),
    });
  }
}

async function requestHandler(req: IncomingMessage, res: ServerResponse): Promise<void> {
  if (!req.url) {
    sendJson(res, 404, { error: "Not found" });
    return;
  }

  if (req.method === "OPTIONS") {
    res.writeHead(204, {
      "Access-Control-Allow-Origin": allowedOrigin,
      "Access-Control-Allow-Methods": "GET,OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type",
    });
    res.end();
    return;
  }

  if (req.method === "GET" && req.url === "/healthz") {
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "GET" && req.url === "/api/syncs") {
    sendJson(res, 200, { recentSyncs });
    return;
  }

  sendJson(res, 404, { error: "Not found" });
}

async function startConsumer(): Promise<void> {
  const url = process.env.KAPTANTO_URL;
  if (!url) {
    throw new Error("KAPTANTO_URL is required");
  }

  while (true) {
    try {
      await consumeKaptantoSse(url, (event) => {
        applyEvent(event).catch((error) => {
          console.error("sync apply failed", error);
        });
      });
    } catch (error) {
      console.error("sync consumer reconnecting", error);
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
  console.log(`sync api listening on ${port}`);
});

startConsumer().catch((error) => {
  console.error(error);
  process.exit(1);
});

