import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";

import { consumeKaptantoSse } from "../../../shared/src/cdc-sse.js";
import type { JsonObject, KaptantoEvent } from "../../../shared/src/cdc-types.js";

type Notification = {
  id: string;
  userId: string;
  title: string;
  body: string;
  createdAt: string;
};

type NotificationState = {
  users: Record<string, { id: string; name: string }>;
  notificationsByUser: Record<string, Notification[]>;
  unreadByUser: Record<string, number>;
  activity: string[];
};

const port = Number(process.env.PORT ?? "4001");
const allowedOrigin = process.env.ALLOWED_ORIGIN ?? "*";
const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const state: NotificationState = {
  users: {},
  notificationsByUser: {},
  unreadByUser: {},
  activity: [],
};
const sseClients = new Set<ServerResponse>();

function getUserName(userId: string): string {
  return state.users[userId]?.name ?? userId;
}

function upsertUser(payload: JsonObject | null | undefined): void {
  if (!payload) return;
  const id = String(payload.id ?? "");
  const name = String(payload.name ?? id);
  if (!id) return;
  state.users[id] = { id, name };
}

function addNotification(userId: string, title: string, body: string, createdAt: string): void {
  const notification: Notification = {
    id: randomUUID(),
    userId,
    title,
    body,
    createdAt,
  };
  state.notificationsByUser[userId] = [notification, ...(state.notificationsByUser[userId] ?? [])].slice(0, 20);
  state.unreadByUser[userId] = (state.unreadByUser[userId] ?? 0) + 1;
}

function applyEvent(event: KaptantoEvent): void {
  if (event.operation === "control") {
    return;
  }

  const after = event.after ?? null;
  if (event.table === "users") {
    upsertUser(after);
  }

  if (!after) {
    return;
  }

  if (event.table === "mentions") {
    const actor = String(after.actor_id);
    const targetUserId = String(after.target_user_id);
    addNotification(
      targetUserId,
      `${getUserName(actor)} mentioned you`,
      "A comment mention triggered this notification from CDC.",
      String(after.created_at ?? new Date().toISOString()),
    );
  }

  if (event.table === "follows") {
    const follower = String(after.follower_id);
    const targetUserId = String(after.target_user_id);
    addNotification(
      targetUserId,
      `${getUserName(follower)} followed you`,
      "Follower events become product notifications without polling.",
      String(after.created_at ?? new Date().toISOString()),
    );
  }

  if (event.table === "comments") {
    state.activity = [
      `${getUserName(String(after.author_id))} commented on ${String(after.post_id)}`,
      ...state.activity,
    ].slice(0, 12);
  }
}

function snapshot() {
  return {
    users: Object.values(state.users),
    notificationsByUser: state.notificationsByUser,
    unreadByUser: state.unreadByUser,
    activity: state.activity,
  };
}

function broadcast(): void {
  const payload = `data: ${JSON.stringify(snapshot())}\n\n`;
  for (const client of sseClients) {
    client.write(payload);
  }
}

async function parseBody(req: IncomingMessage): Promise<JsonObject> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.from(chunk));
  }
  if (chunks.length === 0) {
    return {};
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as JsonObject;
}

function sendJson(res: ServerResponse, status: number, payload: unknown): void {
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Access-Control-Allow-Origin": allowedOrigin,
  });
  res.end(JSON.stringify(payload));
}

async function handleCreateComment(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = randomUUID();
  const mentionId = body.mentionedUserId ? randomUUID() : null;
  const authorId = String(body.authorId ?? "ava");
  const postId = String(body.postId ?? "roadmap-42");
  const text = String(body.body ?? "Hello from CDC");
  const mentionedUserId = body.mentionedUserId ? String(body.mentionedUserId) : null;

  const client = await pool.connect();
  try {
    await client.query("BEGIN");
    await client.query(
      "INSERT INTO comments (id, author_id, post_id, body) VALUES ($1, $2, $3, $4)",
      [id, authorId, postId, text],
    );
    if (mentionId && mentionedUserId) {
      await client.query(
        "INSERT INTO mentions (id, comment_id, actor_id, target_user_id) VALUES ($1, $2, $3, $4)",
        [mentionId, id, authorId, mentionedUserId],
      );
    }
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK");
    throw error;
  } finally {
    client.release();
  }

  sendJson(res, 201, { ok: true, id });
}

async function handleCreateFollow(body: JsonObject, res: ServerResponse): Promise<void> {
  const id = randomUUID();
  await pool.query(
    "INSERT INTO follows (id, follower_id, target_user_id) VALUES ($1, $2, $3)",
    [id, String(body.followerId ?? "morgan"), String(body.targetUserId ?? "ava")],
  );
  sendJson(res, 201, { ok: true, id });
}

async function hydrateUsers(): Promise<void> {
  const result = await pool.query<{ id: string; name: string }>("SELECT id, name FROM users ORDER BY id");
  for (const row of result.rows) {
    state.users[row.id] = row;
    state.notificationsByUser[row.id] ??= [];
    state.unreadByUser[row.id] ??= 0;
  }
}

function handleRead(body: JsonObject, res: ServerResponse): void {
  const userId = String(body.userId ?? "");
  if (!userId) {
    sendJson(res, 400, { error: "userId is required" });
    return;
  }
  state.unreadByUser[userId] = 0;
  broadcast();
  sendJson(res, 200, { ok: true });
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

  if (req.method === "POST" && req.url === "/api/comments") {
    await handleCreateComment(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/follows") {
    await handleCreateFollow(await parseBody(req), res);
    return;
  }

  if (req.method === "POST" && req.url === "/api/notifications/read") {
    handleRead(await parseBody(req), res);
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
        applyEvent(event);
        broadcast();
      });
    } catch (error) {
      console.error("notifications consumer reconnecting", error);
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
  console.log(`notifications api listening on ${port}`);
});

hydrateUsers()
  .then(() => startConsumer())
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
