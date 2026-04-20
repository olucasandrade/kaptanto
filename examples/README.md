# Examples

This directory collects runnable examples that show Kaptanto powering both product features and backend automation.

## Full-Stack Apps

- `notifications`: Postgres comments, mentions, and follows become a live notification inbox.
- `orders-dashboard`: Postgres order, payment, and shipment writes become a live operations board.
- `analytics-feed`: MongoDB activity documents become a live admin feed and rollup counters.

These examples include:

- `api/`: the application backend that writes source data and consumes Kaptanto SSE
- `web/`: a React + Vite frontend that renders CDC-derived product state
- `docker-compose.yml`: the source database, Kaptanto, app API, and web app
- `kaptanto.yaml`: example-specific Kaptanto configuration

## Backend Automation

- `entitlements-sync`: a billing API writes subscription and payment changes to Postgres, and a separate sync API consumes Kaptanto SSE to update an entitlements API automatically.

This example is intentionally API-to-API instead of UI-first. It demonstrates a common CDC pattern: one system owns the source of truth, and downstream systems stay in sync without polling or point-to-point coupling.

## Common Flow

1. A source API writes normal transactional data.
2. Kaptanto watches the source database and emits change events.
3. A consumer service subscribes with a stable consumer ID.
4. The consumer updates projections, calls another API, or drives live UI state.

Start any example from its own folder with:

```bash
docker compose up --build
```

Read the example-local `README.md` for ports, sample requests, and the specific CDC behavior it demonstrates.
