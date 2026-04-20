# Notifications Example

Real-time notification inbox powered by Postgres writes and Kaptanto SSE.

## What It Shows

- Source writes stay simple: comments, follows, and mentions are written normally.
- Kaptanto streams those changes from Postgres.
- The app backend consumes CDC and derives user-facing notifications plus unread counts.
- The frontend gets live inbox updates from the application API.

## Services

- Web: `http://localhost:3001`
- API: `http://localhost:4001`
- Kaptanto SSE: `http://localhost:7654/events`
- Postgres: `localhost:5433`

## Run

```bash
cd examples/notifications
docker compose up --build
```

## Try It

1. Open `http://localhost:3001`.
2. Create a comment with a mention or a follow event.
3. Watch the inbox update without reloading.

## Notes

- The derived notification projection is kept in memory to keep the example easy to read.
- Stable consumer ID `notifications-api` is used so the Kaptanto cursor can resume cleanly.

