# Examples

This directory now centers on two primary visual demos. Both are meant to make the downstream effects of CDC obvious on screen, not just prove that events exist.

## Primary Demos

- `fanout`: a search-and-cache control room where a single product change becomes searchable and cache-consistent almost immediately.
- `audit-trail`: a user-facing recent changes feed where raw row updates become readable product activity.

If you only want to understand what Kaptanto looks like in a real application, start with these two.

These examples include:

- `api/`: the application backend that writes source data and consumes Kaptanto SSE
- `web/`: a React + Vite frontend that renders CDC-derived product state
- `docker-compose.yml`: the source database, Kaptanto, app API, and web app
- `kaptanto.yaml`: example-specific Kaptanto configuration

## Supporting Examples

- `entitlements-sync`: a billing API writes subscription and payment changes to Postgres, and a separate sync API consumes Kaptanto SSE to update an entitlements API automatically.
- `notifications`: Postgres comments, mentions, and follows become a live notification inbox.
- `orders-dashboard`: Postgres order, payment, and shipment writes become a live operations board.
- `analytics-feed`: MongoDB activity documents become a live admin feed and rollup counters.

These remain useful for narrower patterns, but they are no longer the main visual entry point.

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
