# Orders Dashboard Example

Live order operations board powered by Postgres CDC.

## What It Shows

- Order, payment, and shipment writes happen in the transactional database.
- Kaptanto emits those changes immediately.
- The backend consumes CDC and builds a live operational view with recent events.
- The frontend renders a dashboard rather than raw CDC payloads.

## Services

- Web: `http://localhost:3002`
- API: `http://localhost:4002`
- Kaptanto SSE: `http://localhost:7754/events`
- Postgres: `localhost:5434`

## Run

```bash
cd examples/orders-dashboard
docker compose up --build
```

