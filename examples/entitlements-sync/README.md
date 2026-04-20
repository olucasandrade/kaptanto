# Entitlements Sync Example

API-to-API automation powered by Postgres CDC.

## What It Shows

- The billing API owns subscription and payment writes.
- Kaptanto streams `subscriptions` and `invoice_payments` changes from Postgres.
- A separate sync API consumes Kaptanto SSE and calls the entitlements API.
- The entitlements API updates feature access automatically when billing state changes.

## Services

- Billing API: `http://localhost:4010`
- Entitlements API: `http://localhost:4011`
- Sync API: `http://localhost:4012`
- Kaptanto SSE: `http://localhost:7954/events`
- Postgres: `localhost:5435`

## Run

```bash
cd examples/entitlements-sync
docker compose up --build
```

## Try It

Create a subscription:

```bash
curl -X POST http://localhost:4010/api/subscriptions \
  -H 'content-type: application/json' \
  -d '{"customerId":"acme","plan":"pro"}'
```

Mark an invoice as paid:

```bash
curl -X POST http://localhost:4010/api/payments \
  -H 'content-type: application/json' \
  -d '{"customerId":"acme","subscriptionId":"sub_123","status":"paid"}'
```

Check the downstream entitlements state:

```bash
curl http://localhost:4011/api/entitlements
```

## Notes

- The sync API uses a stable consumer ID `entitlements-sync-worker`.
- The entitlements API is kept simple and in-memory so the CDC wiring stays obvious.

