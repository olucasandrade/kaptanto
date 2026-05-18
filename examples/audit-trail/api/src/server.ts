import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";

import { consumeKaptantoSse } from "./cdc-sse.ts";
import type { JsonObject, KaptantoEvent } from "./cdc-types.ts";

type Employee = {
  id: string;
  name: string;
  email: string;
  department: string;
  title: string;
  salaryCents: number;
};

type FieldChange = {
  field: string;
  from: string;
  to: string;
};

type AuditEntry = {
  id: string;
  operation: string;
  employeeId: string;
  employeeName: string;
  changes: FieldChange[];
  timestamp: string;
};

const port = Number(process.env.PORT ?? "4007");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });

const employees = new Map<string, Employee>();
let auditLog: AuditEntry[] = [];
const clients = new Set<ServerResponse>();

function formatField(field: string, raw: unknown): string {
  if (field === "salary_cents") return `$${(Number(raw) / 100).toLocaleString("en-US")}`;
  return String(raw ?? "");
}

function snapshot() {
  return { employees: [...employees.values()], auditLog: auditLog.slice(0, 30) };
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

const AUDITED_FIELDS = ["name", "email", "department", "title", "salary_cents"] as const;

function applyEvent(event: KaptantoEvent): void {
  if (event.operation === "delete" && event.before) {
    const id = String(event.before.id);
    const emp = employees.get(id);
    employees.delete(id);
    auditLog = [
      {
        id: randomUUID(),
        operation: "delete",
        employeeId: id,
        employeeName: emp?.name ?? String(event.before.name ?? id),
        changes: [],
        timestamp: new Date().toISOString(),
      },
      ...auditLog,
    ];
    return;
  }

  if (!event.after) return;
  const after = event.after;
  const id = String(after.id);

  const entry: AuditEntry = {
    id: randomUUID(),
    operation: event.operation,
    employeeId: id,
    employeeName: String(after.name),
    changes: [],
    timestamp: new Date().toISOString(),
  };

  if (event.operation === "update" && event.before) {
    const before = event.before;
    for (const field of AUDITED_FIELDS) {
      if (before[field] !== after[field]) {
        entry.changes.push({
          field,
          from: formatField(field, before[field]),
          to: formatField(field, after[field]),
        });
      }
    }
  }

  if (event.operation === "insert") {
    for (const field of AUDITED_FIELDS) {
      entry.changes.push({ field, from: "—", to: formatField(field, after[field]) });
    }
  }

  employees.set(id, {
    id,
    name: String(after.name),
    email: String(after.email),
    department: String(after.department),
    title: String(after.title),
    salaryCents: Number(after.salary_cents),
  });

  if (event.operation !== "read") {
    auditLog = [entry, ...auditLog].slice(0, 100);
  }
}

async function hydrateEmployees(): Promise<void> {
  const result = await pool.query<{
    id: string; name: string; email: string;
    department: string; title: string; salary_cents: number;
  }>("SELECT id, name, email, department, title, salary_cents FROM employees ORDER BY hired_at");

  for (const row of result.rows) {
    employees.set(row.id, {
      id: row.id,
      name: row.name,
      email: row.email,
      department: row.department,
      title: row.title,
      salaryCents: row.salary_cents,
    });
  }
}

async function hydrateEmployeesWithRetry(): Promise<void> {
  for (let attempt = 1; ; attempt++) {
    try {
      await hydrateEmployees();
      return;
    } catch (err) {
      if (attempt >= 20) {
        throw err;
      }
      console.error(`audit hydrate attempt ${attempt} failed`, err);
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
}

async function startConsumer(): Promise<void> {
  const url = process.env.KAPTANTO_URL;
  if (!url) throw new Error("KAPTANTO_URL is required");

  while (true) {
    try {
      await consumeKaptantoSse(url, (event) => { applyEvent(event); broadcast(); });
    } catch (err) {
      console.error("audit consumer reconnecting", err);
      await new Promise((r) => setTimeout(r, 1500));
    }
  }
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

  const patchMatch = req.url.match(/^\/api\/employees\/([^/]+)$/);
  if (req.method === "PATCH" && patchMatch) {
    const body = await parseBody(req);
    const fields: string[] = [];
    const values: unknown[] = [];
    if (body.department !== undefined) { fields.push(`department = $${fields.length + 1}`); values.push(String(body.department)); }
    if (body.title !== undefined) { fields.push(`title = $${fields.length + 1}`); values.push(String(body.title)); }
    if (body.salaryCents !== undefined) { fields.push(`salary_cents = $${fields.length + 1}`); values.push(Number(body.salaryCents)); }
    if (fields.length === 0) { sendJson(res, 400, { error: "no fields" }); return; }
    fields.push(`updated_at = NOW()`);
    values.push(patchMatch[1]);
    await pool.query(`UPDATE employees SET ${fields.join(", ")} WHERE id = $${values.length}`, values);
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "POST" && req.url === "/api/employees") {
    const body = await parseBody(req);
    const id = randomUUID();
    await pool.query(
      "INSERT INTO employees (id, name, email, department, title, salary_cents) VALUES ($1, $2, $3, $4, $5, $6)",
      [id, String(body.name), String(body.email ?? `${id.slice(0, 6)}@example.com`),
       String(body.department ?? "Engineering"), String(body.title ?? "Engineer"), Number(body.salaryCents ?? 10000000)],
    );
    sendJson(res, 201, { ok: true, id });
    return;
  }

  sendJson(res, 404, { error: "not_found" });
}

createServer((req, res) => {
  requestHandler(req, res).catch((err: unknown) => {
    console.error(err);
    sendJson(res, 500, { error: "internal_error" });
  });
}).listen(port, () => console.log(`audit-trail api listening on ${port}`));

hydrateEmployeesWithRetry()
  .then(() => startConsumer())
  .catch((err) => { console.error(err); process.exit(1); });
