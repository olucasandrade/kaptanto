import { createServer, type IncomingMessage, type ServerResponse } from "node:http";

type JsonObject = Record<string, unknown>;
type EntitlementState = {
  customerId: string;
  subscriptionId: string;
  plan: string;
  active: boolean;
  reason: string;
  updatedAt: string;
};

const port = Number(process.env.PORT ?? "4011");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const entitlements = new Map<string, EntitlementState>();

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

  if (req.method === "GET" && req.url === "/api/entitlements") {
    sendJson(res, 200, { entitlements: [...entitlements.values()] });
    return;
  }

  if (req.method === "POST" && req.url === "/internal/entitlements/sync") {
    const body = await parseBody(req);
    const customerId = String(body.customerId ?? "");
    if (!customerId) {
      sendJson(res, 400, { error: "customerId is required" });
      return;
    }
    const next: EntitlementState = {
      customerId,
      subscriptionId: String(body.subscriptionId ?? ""),
      plan: String(body.plan ?? "starter"),
      active: Boolean(body.active),
      reason: String(body.reason ?? "sync"),
      updatedAt: new Date().toISOString(),
    };
    entitlements.set(customerId, next);
    sendJson(res, 200, { ok: true, entitlement: next });
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
  console.log(`entitlements api listening on ${port}`);
});

