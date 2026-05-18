CREATE TABLE orders (
  id TEXT PRIMARY KEY,
  customer_name TEXT NOT NULL,
  total_cents INT NOT NULL,
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE payments (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES orders(id),
  status TEXT NOT NULL,
  amount_cents INT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE shipments (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES orders(id),
  carrier TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE orders REPLICA IDENTITY FULL;
ALTER TABLE payments REPLICA IDENTITY FULL;
ALTER TABLE shipments REPLICA IDENTITY FULL;

-- ── Seed orders in various pipeline stages ──

-- Stage 1: just created, no payment, no shipment
INSERT INTO orders (id, customer_name, total_cents, status) VALUES
  ('ord-seed-1', 'Northwind Labs',       9200,  'created'),
  ('ord-seed-2', 'Beacon Commerce',      14900, 'created'),
  ('ord-seed-3', 'Pacific Rim Studios',  48500, 'created');

-- Stage 2: payment captured, not yet shipped
INSERT INTO orders (id, customer_name, total_cents, status) VALUES
  ('ord-seed-4', 'Vertex Systems',   32000, 'created'),
  ('ord-seed-5', 'Atlas Digital',    7600,  'created');

INSERT INTO payments (id, order_id, status, amount_cents) VALUES
  ('pay-seed-4', 'ord-seed-4', 'captured', 32000),
  ('pay-seed-5', 'ord-seed-5', 'captured', 7600);

-- Stage 3: payment captured + shipment packed (in packing lane)
INSERT INTO orders (id, customer_name, total_cents, status) VALUES
  ('ord-seed-6', 'Meridian Technologies', 21300, 'created');

INSERT INTO payments (id, order_id, status, amount_cents) VALUES
  ('pay-seed-6', 'ord-seed-6', 'captured', 21300);

INSERT INTO shipments (id, order_id, carrier, status) VALUES
  ('shp-seed-6', 'ord-seed-6', 'DHL Express', 'packed');

-- Stage 4: fully completed
INSERT INTO orders (id, customer_name, total_cents, status) VALUES
  ('ord-seed-7', 'Ironclad Partners',  5500,  'completed'),
  ('ord-seed-8', 'Solaris Energy',     89900, 'completed');

INSERT INTO payments (id, order_id, status, amount_cents) VALUES
  ('pay-seed-7', 'ord-seed-7', 'captured', 5500),
  ('pay-seed-8', 'ord-seed-8', 'captured', 89900);

INSERT INTO shipments (id, order_id, carrier, status) VALUES
  ('shp-seed-7', 'ord-seed-7', 'FedEx',       'delivered'),
  ('shp-seed-8', 'ord-seed-8', 'UPS Freight',  'delivered');
