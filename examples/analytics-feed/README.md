# Analytics Feed Example

Live admin activity feed and counters built from MongoDB CDC.

## What It Shows

- Product activity is written as MongoDB documents.
- Kaptanto watches the MongoDB collection and emits change events.
- The application backend consumes CDC and builds a recent-event feed plus simple rollups.
- The frontend renders a monitoring surface instead of exposing raw CDC payloads directly.

## Services

- Web: `http://localhost:3003`
- API: `http://localhost:4003`
- Kaptanto SSE: `http://localhost:7854/events`
- MongoDB: `localhost:27018`

## Run

```bash
cd examples/analytics-feed
docker compose up --build
```

