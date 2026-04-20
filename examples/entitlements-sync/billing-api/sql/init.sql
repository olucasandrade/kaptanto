CREATE TABLE subscriptions (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL,
  plan TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE invoice_payments (
  id TEXT PRIMARY KEY,
  subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
  customer_id TEXT NOT NULL,
  status TEXT NOT NULL,
  amount_cents INT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE subscriptions REPLICA IDENTITY FULL;
ALTER TABLE invoice_payments REPLICA IDENTITY FULL;

INSERT INTO subscriptions (id, customer_id, plan, status)
VALUES ('sub_seed_acme', 'acme', 'starter', 'trialing');

