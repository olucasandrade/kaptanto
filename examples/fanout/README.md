# Fan-Out Example

Search and recommendation side systems kept in sync with near-zero drift.

## What It Shows

- A single Postgres `products` table acts as the source of truth.
- Kaptanto streams every product change once.
- Multiple independent consumers build different derived views from the same CDC stream.
- One of those consumers maintains a search index continuously, so new or updated products become searchable almost immediately.

## Services

- Web: `http://localhost:3006`
- API: `http://localhost:4006`
- Kaptanto SSE: `http://localhost:7164/events`
- Postgres: `localhost:5437`

## Run

```bash
cd examples/fanout
docker compose up --build
```

## Try It

1. Open `http://localhost:3006`.
2. Add a product or update its name, category, price, or stock level.
3. Watch three downstream views update independently:
   inventory alerts,
   pricing history,
   and the derived search index.
4. Confirm that changed products appear in the search-side projection without waiting for a batch sync.

## Why It Matters

- This is the pattern behind search, recommendation, and indexing systems that should not drift from the primary database.
- The difference is not only real-time feel. It is reliability: the same durable CDC stream drives every downstream consumer.
- Each consumer has its own cursor, so search indexing can recover independently without coupling itself to pricing or inventory logic.

## Notes

- The example starts three independent consumers in the API process: `inventory-service`, `search-indexer`, and `price-monitor`.
- The search projection is intentionally simple and in-memory, but the wiring matches how you would feed Elasticsearch, OpenSearch, Typesense, or a recommendation pipeline.
