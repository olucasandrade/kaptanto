# Audit Trail Example

User-facing "recent changes everywhere" product powered by Postgres CDC.

## What It Shows

- Normal product writes happen in the source database: employee records are created, updated, or deleted.
- Kaptanto streams those row changes from Postgres as structured events.
- The app backend consumes CDC and derives a recent-changes feed that is readable by end users, not just operators.
- The frontend renders a product activity surface similar to the "John changed pricing" or "Sarah moved task" pattern used in collaboration tools.

## Services

- Web: `http://localhost:3007`
- API: `http://localhost:4007`
- Kaptanto SSE: `http://localhost:7264/events`
- Postgres: `localhost:5438`

## Run

```bash
cd examples/audit-trail
docker compose up --build
```

## Try It

1. Open `http://localhost:3007`.
2. Change an employee's department, title, or salary.
3. Watch the recent-changes feed update immediately with a derived audit entry.
4. Add a new employee and see the product activity timeline expand without polling.

## Why It Matters

- This is the pattern behind "recent changes" experiences across product surfaces.
- CDC keeps the activity feed tied to the primary database instead of a second write path.
- The value is not just speed. It is consistency between the source of truth and what users see in the feed.

## Notes

- The consumer uses a stable SSE consumer ID via `consumer=audit-api`.
- The example keeps the derived audit projection in memory so the CDC flow stays easy to inspect.
