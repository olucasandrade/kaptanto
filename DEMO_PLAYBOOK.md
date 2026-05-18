# Kaptanto Demo Playbook

A content guide for creating videos and demos about CDC and Kaptanto. Each section covers one piece of content: what to say, what to run, and what to show.

---

## Prerequisites (run once before any demo)

```bash
# Build the binary
make build

# Verify it works
./kaptanto --version

# Docker must be running
docker info
```

Terminal font size: **18–20pt**. Split your screen: terminal on the left, browser on the right whenever you show a UI.

---

## Content Map

| # | Title | Format | Length | What it shows |
|---|-------|--------|--------|---------------|
| 1 | What is CDC? | Explainer (no code) | 3–5 min | Concept foundation |
| 2 | Your first CDC stream | Terminal demo | 4–6 min | Raw stdout with jq |
| 3 | Live orders dashboard | Full demo | 6–8 min | orders-dashboard example |
| 4 | Real-time notification inbox | Full demo | 6–8 min | notifications example |
| 5 | Fan-out: one stream, three consumers | Full demo | 6–8 min | fanout example |
| 6 | Audit trail out of CDC | Full demo | 5–7 min | audit-trail example |
| 7 | Entitlements sync across services | Full demo | 5–7 min | entitlements-sync example |
| 8 | CDC from MongoDB | Full demo | 5–7 min | analytics-feed example |
| 9 | Cursor resume: never miss a change | Terminal demo | 4–6 min | cursor-resume example |
| 10 | Filtering and column projection | Terminal demo | 4–5 min | --where, --columns flags |

---

## Video 1 — What is CDC?

**Format:** Slides / diagram walkthrough, no live code.

### Hook (10 seconds)
> "Every product problem that sounds like a cache problem is actually a CDC problem. Let me show you why."

### Narrative script

**Slide 1 — The polling problem**
> "Imagine you have a Postgres database and you need another system — a search index, a cache, a notification service — to react when rows change. The naive approach is to poll: run a SELECT every few seconds and look for new data. Polling is slow, wasteful, and always slightly stale. You are constantly asking 'did anything change?' when the database already knows."

**Slide 2 — The two-write problem**
> "So you try something smarter: write to the database and also write to Kafka or Redis at the same time. Two writes. But now you have a distributed transaction problem. What happens when the first write succeeds and the second fails? Your systems are out of sync and you have no clean way to fix it."

**Slide 3 — What CDC does**
> "CDC solves this by reading the database transaction log — the WAL in Postgres, the Oplog in MongoDB. Every write is recorded there before it is considered committed. CDC tails that log and emits events in real time. You write once to the database. Everything downstream reacts. No polling. No double-write. No sync drift."

**Diagram to show:**
```
 App ──► Postgres  ──► WAL (transaction log)
                              │
                         CDC tool (Kaptanto)
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
         Search index    Notification     Cache
                           service       invalidation
```

**Slide 4 — What Kaptanto is**
> "Kaptanto is a single static Go binary that tails the Postgres WAL or MongoDB Change Streams and streams those events via stdout, SSE, or gRPC. No JVM, no Kafka required, no sidecars. You point it at a database and it starts streaming."

**Closing line:**
> "In the next video I'll show you what that looks like in practice — a live Postgres stream in about two minutes."

---

## Video 2 — Your First CDC Stream

**Format:** Terminal demo.

### Hook
> "I'm going to point Kaptanto at a Postgres database and watch changes appear in real time. From zero to live stream in under two minutes."

### Setup (before recording)

```bash
# Start a throwaway Postgres with logical replication enabled
docker run --rm -d \
  --name kaptanto-demo \
  -e POSTGRES_DB=demo \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -p 5440:5432 \
  postgres:16-alpine \
  postgres -c wal_level=logical
```

Wait a few seconds for Postgres to start, then:

```bash
# Create a table and set REPLICA IDENTITY FULL
docker exec -i kaptanto-demo psql -U postgres -d demo <<'SQL'
CREATE TABLE products (
  id    SERIAL PRIMARY KEY,
  name  TEXT   NOT NULL,
  price INT    NOT NULL,
  stock INT    NOT NULL DEFAULT 0
);
ALTER TABLE products REPLICA IDENTITY FULL;
SQL
```

### Recording script

**Terminal 1 — start Kaptanto**
```bash
./kaptanto \
  --source "postgres://postgres:postgres@localhost:5440/demo" \
  --tables public.products \
  --output stdout | jq .
```

**Say:**
> "I'm starting Kaptanto pointed at a local Postgres. The `--output stdout` flag means every change comes out as a JSON line. I'm piping through jq just to pretty-print."
>
> "Notice the first event — that's the control event telling us backfill is ready. From here, any write to the `products` table will appear instantly."

**Terminal 2 — make writes**
```bash
docker exec -i kaptanto-demo psql -U postgres -d demo <<'SQL'
INSERT INTO products (name, price, stock) VALUES ('Widget A', 1999, 100);
SQL
```

**Say:**
> "There it is — an insert event. You can see `op: insert`, the `after` object with all the column values, the table name, and the commit timestamp. No polling. This appeared the moment Postgres committed the transaction."

```bash
docker exec -i kaptanto-demo psql -U postgres -d demo <<'SQL'
UPDATE products SET stock = 85 WHERE name = 'Widget A';
SQL
```

**Say:**
> "An update. Notice there's a `before` field with the old values and an `after` field with the new ones. That's because we set `REPLICA IDENTITY FULL` on this table. Without that, Postgres only gives you the primary key in `before`."

```bash
docker exec -i kaptanto-demo psql -U postgres -d demo <<'SQL'
DELETE FROM products WHERE name = 'Widget A';
SQL
```

**Say:**
> "And a delete. The `before` field has the full row that was deleted. `after` is null."

**Filter the output live:**
```bash
# In a third terminal or after stopping the first stream
./kaptanto \
  --source "postgres://postgres:postgres@localhost:5440/demo" \
  --tables public.products \
  --output stdout | jq 'select(.operation == "update")'
```

**Say:**
> "You can filter on the consumer side with jq, or you can tell Kaptanto to filter at the source. Either way, you only process what you care about."

### Cleanup
```bash
docker stop kaptanto-demo
```

---

## Video 3 — Live Orders Dashboard

**Format:** Full demo with browser UI.

**Example:** `examples/orders-dashboard`

### Hook
> "Here's a real-world pattern: an operational orders board that updates the moment your database changes — no WebSocket server required, no polling, no custom pub/sub. Just CDC."

### Setup

```bash
cd examples/orders-dashboard
docker compose up --build
```

Wait until you see kaptanto logs showing `backfill complete` or similar. Open:
- `http://localhost:3002` — the dashboard

### Recording script

**Show the dashboard first (browser)**

> "This is a live orders board. It already has seeded data — orders in different pipeline stages. Let me show you what happens when something changes in the database."

**Open a second terminal:**
```bash
docker exec -i $(docker compose -f examples/orders-dashboard/docker-compose.yml ps -q postgres) \
  psql -U postgres -d orders
```

**Insert a new order:**
```sql
INSERT INTO orders (id, customer_name, total_cents, status)
VALUES ('ord-live-1', 'Demo Corp', 52000, 'created');
```

> "Watch the dashboard. New order, Demo Corp, just appeared. I didn't touch the API. I wrote directly to the database — Kaptanto picked it up from the WAL and pushed it to the dashboard."

**Capture payment:**
```sql
INSERT INTO payments (id, order_id, status, amount_cents)
VALUES ('pay-live-1', 'ord-live-1', 'captured', 52000);
```

> "Payment captured. The dashboard updated the payment status on that order — all derived from the CDC stream. The app backend is just consuming SSE from Kaptanto and building this view."

**Add shipment:**
```sql
INSERT INTO shipments (id, order_id, carrier, status)
VALUES ('shp-live-1', 'ord-live-1', 'FedEx', 'packed');
```

> "Shipment created. Now update it to delivered:"

```sql
UPDATE shipments SET status = 'delivered' WHERE id = 'shp-live-1';
```

> "Delivered. That whole pipeline — create, payment, ship, deliver — happened through plain SQL writes. Kaptanto handled the rest."

**Show the Kaptanto SSE stream raw:**
```bash
curl -N "http://localhost:7754/events?consumer=demo-viewer" | head -20
```

> "This is what the dashboard is actually consuming. Plain SSE — you can connect to it from a browser, a Node.js process, a Python script, anything. No special SDK needed."

**Show the kaptanto config:**
```bash
cat examples/orders-dashboard/kaptanto.yaml
```

> "The entire Kaptanto config for this demo. A source, an output mode, and a list of tables. That's it. Three tables, one binary, one live dashboard."

---

## Video 4 — Real-time Notification Inbox

**Format:** Full demo with browser UI.

**Example:** `examples/notifications`

### Hook
> "The notification inbox is one of the hardest UI patterns to get right without polling. Let me show you how CDC eliminates the complexity."

### Setup

```bash
cd examples/notifications
docker compose up --build
```

Open `http://localhost:3001`.

### Recording script

**Show the inbox (browser)**

> "There's already a seeded inbox — comments and mentions from the initial database state. Kaptanto ran a snapshot when it started and delivered all existing rows as read events. The inbox was populated without a single API call."

**Open Postgres:**
```bash
docker exec -i $(docker compose -f examples/notifications/docker-compose.yml ps -q postgres) \
  psql -U postgres -d notifications
```

**Create a new user and a mention:**
```sql
INSERT INTO users (id, name) VALUES ('carlos', 'Carlos Reyes');

INSERT INTO comments (id, author_id, post_id, body)
VALUES ('cmt-demo-1', 'ava', 'sprint-42', 'Can you take the API review @carlos?');

INSERT INTO mentions (id, comment_id, actor_id, target_user_id)
VALUES ('men-demo-1', 'cmt-demo-1', 'ava', 'carlos');
```

> "Watch the inbox. A new notification for Carlos. Ava mentioned him in a comment. The app received that from Kaptanto and derived the notification — no notification write path, no event bus, just the primary database writes and CDC."

**Add a follow:**
```sql
INSERT INTO follows (id, follower_id, target_user_id)
VALUES ('flw-demo-1', 'morgan', 'carlos');
```

> "Another notification: Morgan followed Carlos. This is the classic fan engagement flow — one insert in the `follows` table triggers a notification for the target user."

**Show the unread count updating:**
> "Notice the unread count badge. It reflects the actual state from CDC — every time the inbox changes, the count updates. No dedicated unread-count endpoint required."

**Key talking point:**
> "The important thing here is what's NOT happening. There's no notification queue, no write to a separate notifications table, no Redis pub/sub. The source tables are `users`, `comments`, `mentions`, and `follows` — totally normal application tables. Kaptanto watches them and the app derives everything else."

---

## Video 5 — Fan-out: One Stream, Three Consumers

**Format:** Full demo with browser UI.

**Example:** `examples/fanout`

### Hook
> "Fan-out is where CDC becomes genuinely powerful. One change in your database, multiple downstream systems update. And each one is independent — they can fall behind and catch up without affecting each other."

### Setup

```bash
cd examples/fanout
docker compose up --build
```

Open `http://localhost:3006`.

### Recording script

**Show the three panels:**
> "This dashboard has three sections driven by the same CDC stream: an inventory view, a pricing history tracker, and a search index. Same source table — `products` — three independent consumers."

**Open Postgres:**
```bash
docker exec -i $(docker compose -f examples/fanout/docker-compose.yml ps -q postgres) \
  psql -U postgres -d fanout
```

**Add a product:**
```sql
INSERT INTO products (id, name, category, price_cents, stock_quantity, description)
VALUES (
  gen_random_uuid()::text,
  'Carbon Fiber Frame',
  'components',
  189900,
  12,
  'Professional-grade carbon fiber bicycle frame.'
);
```

> "One insert. Watch all three panels: the inventory view picked it up, the pricing tracker has it, and the search index already has it indexed. Three consumers, one event."

**Update the price:**
```sql
UPDATE products SET price_cents = 174900 WHERE name = 'Carbon Fiber Frame';
```

> "Price changed. The pricing history panel shows the change with the old and new values. The inventory panel updated. Search index refreshed. The event was emitted once from Kaptanto and each consumer handled it independently."

**Update stock:**
```sql
UPDATE products SET stock_quantity = 3 WHERE name = 'Carbon Fiber Frame';
```

> "Stock dropped to 3. The inventory view flags it as low stock. The search index updated. Pricing history — unchanged, because the price didn't move."

**Key talking point:**
> "This is the real CDC promise. In a traditional architecture you'd write to the database AND publish to Kafka AND update Redis AND call the search API. Any of those can fail. With CDC, you write once to the database. Kaptanto reads the WAL and delivers to each consumer. If the search consumer is slow, it has its own cursor and will catch up. It cannot fall behind and miss events."

**Show consumer cursors:**
```bash
curl "http://localhost:7164/events?consumer=inventory-service" &
curl "http://localhost:7164/events?consumer=search-indexer" &
curl "http://localhost:7164/events?consumer=price-monitor" &
```

> "Three separate SSE connections. Each has its own cursor in Kaptanto. One stream, three independent positions."

---

## Video 6 — Audit Trail Out of CDC

**Format:** Full demo with browser UI.

**Example:** `examples/audit-trail`

### Hook
> "Most teams build audit trails as a second write path: log every action to a separate table. That drifts. CDC gives you an audit trail that is guaranteed to match the source of truth."

### Setup

```bash
cd examples/audit-trail
docker compose up --build
```

Open `http://localhost:3007`.

### Recording script

**Show the activity feed:**
> "This is a user-facing 'recent changes' feed — the kind you see in collaboration tools: 'Sarah changed pricing', 'John moved task'. It's built entirely from CDC on the `employees` table."

**Open Postgres:**
```bash
docker exec -i $(docker compose -f examples/audit-trail/docker-compose.yml ps -q postgres) \
  psql -U postgres -d audittrail
```

**Add an employee:**
```sql
INSERT INTO employees (id, name, department, title, salary_cents)
VALUES (gen_random_uuid()::text, 'Jordan Kim', 'Engineering', 'Senior Engineer', 18500000);
```

> "New employee. Watch the feed — it shows 'Jordan Kim joined Engineering as Senior Engineer'. That entry was not written anywhere. It was derived from the CDC event by the application."

**Promote them:**
```sql
UPDATE employees SET title = 'Staff Engineer', salary_cents = 21000000
WHERE name = 'Jordan Kim';
```

> "Promotion. The feed shows the title change. The salary change is intentionally not shown — we can control what the application exposes without touching the database schema."

**Move to a different department:**
```sql
UPDATE employees SET department = 'Platform'
WHERE name = 'Jordan Kim';
```

> "Department change. The audit trail is consistent with the database because it IS the database — derived from the transaction log, not from a separate write."

**Delete the record:**
```sql
DELETE FROM employees WHERE name = 'Jordan Kim';
```

> "Deletion is captured. The `before` field in the CDC event has the full row even after it's gone from the database, because we set REPLICA IDENTITY FULL."

**Key talking point:**
> "If you build an audit trail as a trigger or a second write, you have two sources of truth. Someone can write SQL directly and bypass your audit. CDC reads the WAL. There is no bypass. Every write — ORM, migration, direct SQL — appears in the stream."

---

## Video 7 — Entitlements Sync Across Services

**Format:** Full demo, terminal-focused.

**Example:** `examples/entitlements-sync`

### Hook
> "The billing service knows when a customer upgrades. The entitlements service controls feature access. Keeping them in sync without coupling them is a classic distributed systems problem. CDC solves it cleanly."

### Setup

```bash
cd examples/entitlements-sync
docker compose up --build
```

### Recording script

**Explain the architecture:**
> "Three services: billing owns the database with `subscriptions` and `invoice_payments` tables. An entitlements API controls feature flags per customer. A sync worker consumes Kaptanto SSE and calls the entitlements API whenever billing state changes. No direct coupling between billing and entitlements."

**Create a subscription (trial):**
```bash
curl -s -X POST http://localhost:4010/api/subscriptions \
  -H 'content-type: application/json' \
  -d '{"customerId":"acme","plan":"pro"}' | jq .
```

**Check entitlements:**
```bash
curl -s http://localhost:4011/api/entitlements | jq .
```

> "Acme is now on pro trial. The billing service wrote to its database. Kaptanto saw the insert, the sync worker received it via SSE and called the entitlements API. No direct call from billing to entitlements."

**Mark invoice as paid:**
```bash
curl -s -X POST http://localhost:4010/api/payments \
  -H 'content-type: application/json' \
  -d '{"customerId":"acme","subscriptionId":"sub_acme","status":"paid"}' | jq .
```

**Check entitlements again:**
```bash
curl -s http://localhost:4011/api/entitlements | jq .
```

> "Payment confirmed. Entitlements updated to active. The sync worker received the `invoice_payments` insert and called the entitlements API. Billing never called entitlements directly."

**Show what happens on failure:**
> "If the entitlements API is down, the sync worker will fail to process the event. But Kaptanto holds the cursor — the sync worker hasn't advanced past that event. When the entitlements API recovers, the worker reconnects and processes the event. At-least-once delivery with automatic resume."

**Key talking point:**
> "This is event-driven architecture without a message broker. No Kafka, no RabbitMQ. The database IS the event source. Kaptanto makes the WAL consumable as SSE. Your sync worker is just an HTTP server that subscribes to a stream."

---

## Video 8 — CDC from MongoDB

**Format:** Full demo with browser UI.

**Example:** `examples/analytics-feed`

### Hook
> "Kaptanto works with MongoDB Change Streams too. Same output format, same SSE API, same consumer pattern — just point it at a Mongo replica set."

### Setup

```bash
cd examples/analytics-feed
docker compose up --build
```

Open `http://localhost:3003`.

### Recording script

**Show the dashboard:**
> "This is a live activity feed powered by MongoDB CDC. Product events are written as documents. Kaptanto watches the `product_events` collection via Change Streams and the application builds a live feed."

**Insert events via the API:**
```bash
curl -s -X POST http://localhost:4003/api/events \
  -H 'content-type: application/json' \
  -d '{"type":"product_viewed","productId":"prod-01","userId":"u-42","meta":{"referrer":"search"}}' | jq .
```

> "Product viewed event. Watch the feed — it appeared immediately."

```bash
curl -s -X POST http://localhost:4003/api/events \
  -H 'content-type: application/json' \
  -d '{"type":"product_purchased","productId":"prod-01","userId":"u-42","meta":{"quantity":2}}' | jq .
```

> "Purchase event. The activity feed and the rollup counters both updated."

**Insert multiple events rapidly:**
```bash
for i in $(seq 1 5); do
  curl -s -X POST http://localhost:4003/api/events \
    -H 'content-type: application/json' \
    -d "{\"type\":\"product_viewed\",\"productId\":\"prod-0${i}\",\"userId\":\"u-${i}\",\"meta\":{}}" &
done
wait
```

> "Five concurrent events. All five appear in the feed in order."

**Show the Kaptanto config:**
```bash
cat examples/analytics-feed/kaptanto.yaml
```

> "The only difference from the Postgres examples is the source URI — `mongodb://` instead of `postgres://`. Everything else is identical. Same SSE output, same consumer cursors, same filtering API."

**Key talking point:**
> "This matters if you're running a polyglot stack. Postgres for transactional data, MongoDB for unstructured events. Kaptanto gives you a unified CDC stream from both. Your consumers don't know or care which database the event came from."

---

## Video 9 — Cursor Resume: Never Miss a Change

**Format:** Terminal demo.

**Example:** `examples/cursor-resume`

### Hook
> "What happens when your consumer disconnects — a deploy, a crash, a restart? With polling, you miss events. With CDC cursors, you resume exactly where you left off."

### Setup

```bash
cd examples/cursor-resume
docker compose up --build
```

Open `http://localhost:3005`.

### Recording script

**Show the running dashboard:**
> "This is a job board. It's connected to Kaptanto via SSE with a stable consumer ID. That consumer ID is the key — Kaptanto persists the delivery cursor for that ID."

**Insert some jobs:**
```bash
docker exec -i $(docker compose -f examples/cursor-resume/docker-compose.yml ps -q postgres) \
  psql -U postgres -d cursorresume <<'SQL'
INSERT INTO jobs (id, title, company, status)
VALUES
  ('job-01', 'Staff Engineer', 'Acme Corp', 'open'),
  ('job-02', 'Product Designer', 'Beacon Labs', 'open');
SQL
```

> "Two jobs appeared. Now let me simulate a consumer restart."

**Stop just the API (not Kaptanto):**
```bash
docker compose -f examples/cursor-resume/docker-compose.yml stop api
```

> "The API is down. While it's down, I'll insert more changes:"

```bash
docker exec -i $(docker compose -f examples/cursor-resume/docker-compose.yml ps -q postgres) \
  psql -U postgres -d cursorresume <<'SQL'
INSERT INTO jobs (id, title, company, status)
VALUES ('job-03', 'Data Engineer', 'Meridian', 'open');
UPDATE jobs SET status = 'closed' WHERE id = 'job-01';
SQL
```

> "A new job and a status change. The API is still down — it missed these in real time."

**Restart the API:**
```bash
docker compose -f examples/cursor-resume/docker-compose.yml start api
```

> "The API reconnected. Watch the dashboard — the new job appears, and job-01 is now closed. Kaptanto held those events and delivered them the moment the consumer reconnected. Nothing was lost."

**Show the cursor in action:**
```bash
# Show the SSE connection with consumer ID
curl -N "http://localhost:7064/events?consumer=cursor-api&tables=jobs" | head -5
```

> "The consumer ID `cursor-api` is what Kaptanto tracks. Reconnect with the same ID from anywhere and you resume from the last acknowledged event. The cursor is server-side — your consumer is stateless."

---

## Video 10 — Filtering and Column Projection

**Format:** Terminal demo.

### Hook
> "CDC streams everything by default. In production you usually want to filter by table, operation, or SQL condition. You might also want to drop sensitive columns before they hit your consumers."

### Setup

```bash
# Use the throwaway Postgres from Video 2
docker run --rm -d \
  --name kaptanto-filter-demo \
  -e POSTGRES_DB=shop \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -p 5441:5432 \
  postgres:16-alpine \
  postgres -c wal_level=logical

docker exec -i kaptanto-filter-demo psql -U postgres -d shop <<'SQL'
CREATE TABLE orders (
  id         SERIAL PRIMARY KEY,
  customer   TEXT   NOT NULL,
  email      TEXT   NOT NULL,
  total_cents INT   NOT NULL,
  status     TEXT   NOT NULL DEFAULT 'pending'
);
CREATE TABLE logs (
  id      SERIAL PRIMARY KEY,
  message TEXT NOT NULL
);
ALTER TABLE orders REPLICA IDENTITY FULL;
ALTER TABLE logs   REPLICA IDENTITY FULL;
SQL
```

### Recording script

**1. Watch only one table**
```bash
./kaptanto \
  --source "postgres://postgres:postgres@localhost:5441/shop" \
  --tables public.orders \
  --output stdout | jq .
```

> "Even though the database has multiple tables, `--tables public.orders` means Kaptanto only tracks that one. Writes to `logs` won't appear."

**In a second terminal — write to both:**
```bash
docker exec -i kaptanto-filter-demo psql -U postgres -d shop <<'SQL'
INSERT INTO orders (customer, email, total_cents) VALUES ('Alice', 'alice@example.com', 4900);
INSERT INTO logs (message) VALUES ('some internal log message');
SQL
```

> "Only the orders event came through. The logs insert was ignored entirely."

**2. Column filtering (drop PII)**
```bash
./kaptanto \
  --source "postgres://postgres:postgres@localhost:5441/shop" \
  --tables public.orders \
  --columns "id,customer,total_cents,status" \
  --output stdout | jq .
```

**Write again:**
```bash
docker exec -i kaptanto-filter-demo psql -U postgres -d shop <<'SQL'
INSERT INTO orders (customer, email, total_cents) VALUES ('Bob', 'bob@example.com', 12000);
SQL
```

> "The `email` column is gone. We stripped it at the CDC layer — it never leaves the Kaptanto process. Your downstream consumers never see it."

**3. SQL WHERE filter**

```bash
./kaptanto \
  --source "postgres://postgres:postgres@localhost:5441/shop" \
  --tables public.orders \
  --where "status != 'cancelled'" \
  --output stdout | jq .
```

**Write a cancelled order:**
```bash
docker exec -i kaptanto-filter-demo psql -U postgres -d shop <<'SQL'
INSERT INTO orders (customer, email, total_cents, status) VALUES ('Carol', 'carol@example.com', 3000, 'cancelled');
INSERT INTO orders (customer, email, total_cents, status) VALUES ('Dave', 'dave@example.com', 7800, 'pending');
SQL
```

> "Dave's pending order came through. Carol's cancelled order was filtered at source. The `--where` clause runs against each row before it reaches any consumer."

**4. Config file equivalent**
```bash
cat <<'EOF' > /tmp/shop.yaml
source: postgres://postgres:postgres@localhost:5441/shop
output: sse
port: 7655
tables:
  public.orders:
    columns: [id, customer, total_cents, status]
    where: "status != 'cancelled'"
EOF

./kaptanto --config /tmp/shop.yaml
```

> "Everything you can pass as a flag, you can put in YAML. In production you'll use a config file. The CLI flags are great for quick experiments."

### Cleanup
```bash
docker stop kaptanto-filter-demo
```

---

## General Recording Tips

### Terminal setup
- Font: JetBrains Mono or similar, 18–20pt
- Theme: dark background (One Dark or Catppuccin Mocha)
- Shell: `PS1='$ '` — keep the prompt minimal
- Width: 120 columns

### Split layout for UI demos
```
┌─────────────────────────┬─────────────────────────┐
│                         │                         │
│  Terminal (commands)    │  Browser (UI / output)  │
│                         │                         │
└─────────────────────────┴─────────────────────────┘
```

### Talking rhythm
- Run the command, pause for 1 second, then speak while the output appears.
- Never explain what you're about to type — say it as you type it or after the result appears.
- When an event arrives in the terminal, point to the specific field you want to highlight.

### Common phrasing patterns
- "Watch what happens when I..." → then write the SQL
- "That event came from the WAL, not from a trigger, not from a second write"
- "Nothing changed in my application code — Kaptanto is reading the log the database already writes"
- "Each consumer has its own cursor. This one can fall behind without affecting the others."

### Live `jq` filters worth showing on camera

```bash
# Pretty-print with color
./kaptanto ... | jq .

# Show only the operation and table
./kaptanto ... | jq '{op: .operation, table: .table}'

# Show only inserts
./kaptanto ... | jq 'select(.operation == "insert")'

# Show the after values only
./kaptanto ... | jq '.after'

# Count events
./kaptanto ... | jq -c . | wc -l
```

### Useful curl patterns for live demos

```bash
# Raw SSE stream
curl -N "http://localhost:7654/events?consumer=my-viewer"

# Filter to one table via query param
curl -N "http://localhost:7654/events?consumer=my-viewer&tables=orders"

# Filter to inserts only
curl -N "http://localhost:7654/events?consumer=my-viewer&operations=insert"

# Health check
curl http://localhost:7654/healthz

# Prometheus metrics
curl -s http://localhost:7654/metrics | grep kaptanto_
```

---

## Short-form Content Ideas (30–60 seconds)

These are quick clips, not full demos. Good for Twitter/LinkedIn posts.

| Clip | What to show | Command |
|------|-------------|---------|
| "CDC in 30 seconds" | Insert → event appears | Videos 2 setup + one INSERT |
| "Before and after" | UPDATE with REPLICA IDENTITY FULL | Show `before` and `after` fields side by side |
| "Column stripping" | Insert with email → event without email | `--columns` flag demo |
| "Cursor resume" | Stop API, insert, restart, event arrives | Condensed Video 9 |
| "One write, three consumers" | Fan-out all three panels update | Best part of Video 5 |
| "Static binary" | `ls -lh ./kaptanto` then `./kaptanto --help` | No Docker needed |
