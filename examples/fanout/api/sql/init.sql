CREATE TABLE products (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  category TEXT NOT NULL,
  price_cents INT NOT NULL,
  stock_quantity INT NOT NULL DEFAULT 0,
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE products REPLICA IDENTITY FULL;

INSERT INTO products (id, name, category, price_cents, stock_quantity, description) VALUES
  ('prod-1', 'Wireless Headphones',   'Electronics', 14900,  42,  'Noise-cancelling over-ear headphones'),
  ('prod-2', 'Ergonomic Chair',       'Furniture',   59900,   8,  'Mesh back office chair'),
  ('prod-3', 'Standing Desk',         'Furniture',   89900,   3,  'Height-adjustable electric desk'),
  ('prod-4', 'USB-C Hub',             'Electronics',  4900, 156,  '7-port multifunction hub'),
  ('prod-5', 'Mechanical Keyboard',   'Electronics', 12900,  27,  'Tenkeyless with tactile switches'),
  ('prod-6', 'Monitor Arm',           'Furniture',   18900,  14,  'Single arm VESA mount'),
  ('prod-7', 'Webcam 4K',             'Electronics',  9900,   5,  '4K 30fps USB webcam');
