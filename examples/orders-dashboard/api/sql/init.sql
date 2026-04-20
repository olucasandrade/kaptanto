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

INSERT INTO orders (id, customer_name, total_cents, status)
VALUES ('ord-seed-1', 'Northwind Labs', 9200, 'created');

