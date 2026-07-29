# Local RAG with pgvector + Ollama

Zero-cloud retrieval-augmented generation: Postgres CDC → Kaptanto vector sink →
local Ollama embeddings → pgvector similarity search. No cloud API keys.

## What It Shows

- `pgvector/pgvector` runs as both the CDC source and the vector store.
- Ollama serves `nomic-embed-text` on an OpenAI-compatible `/v1` endpoint.
- Kaptanto `output: vector` embeds `title` + `body` and upserts into `kaptanto_vectors`.
- A similarity query returns the closest documents after seed/backfill.

## Architecture

```
documents (Postgres+pgvector)
  → Kaptanto (output: vector)
  → Ollama nomic-embed-text (local)
  → kaptanto_vectors (pgvector)
  → cosine similarity SQL
```

## Prerequisites

- Docker & Docker Compose
- `jq` and `psql` on the host (for `./similarity.sh`)
- ~1 GB disk for the Ollama model pull (first run)

## Run

**1. Start the stack** (pulls `nomic-embed-text` locally — no cloud):

```bash
cd examples/rag-pgvector
docker compose up --build -d
```

Wait until `ollama-pull` completes and Kaptanto is healthy:

```bash
docker compose ps
curl -sf http://localhost:7661/healthz
```

**2. Confirm vectors were written** (seed rows + backfill):

```bash
psql postgres://postgres:postgres@localhost:5441/rag -c \
  'SELECT id, left(text, 60) AS preview, metadata FROM kaptanto_vectors;'
```

You should see three rows (one per seeded document).

**3. Similarity query after seed:**

```bash
chmod +x ./similarity.sh
./similarity.sh "local RAG with vectors"
```

Expected: `Local RAG with pgvector` ranks near the top (highest `score`).

Or paste the SQL shape from `query.sql` after embedding a query with:

```bash
curl -sf http://localhost:11434/api/embeddings \
  -H 'Content-Type: application/json' \
  -d '{"model":"nomic-embed-text","prompt":"local RAG with vectors"}'
```

**4. Live update** — insert a new document and re-query:

```bash
psql postgres://postgres:postgres@localhost:5441/rag -c \
  "INSERT INTO documents (title, body, category) VALUES
   ('Shipping webhooks', 'Deliver CDC events to Lambda with SigV4 async invoke.', 'ops');"
# wait a few seconds for embed+upsert, then:
./similarity.sh "invoke lambda from CDC"
```

## Services

| Service | URL |
|---------|-----|
| Postgres + pgvector | localhost:5441 |
| Ollama | http://localhost:11434 |
| Kaptanto health | http://localhost:7661/healthz |

## Configuration

```yaml
output: vector
sinks:
  vector:
    source:
      columns: [title, body]
    embedder:
      provider: openai
      base-url: http://ollama:11434/v1
      model: nomic-embed-text
      dimensions: 768
    store:
      provider: pgvector
      dsn: ${VECTOR_DSN}
```

- `embedder.api-key` may be empty for local Ollama.
- `store.dsn` must be a `${VAR}` reference.
- `dimensions: 768` matches `nomic-embed-text` (required for pgvector DDL).

## Zero-cloud guarantee

This example never calls OpenAI, Cohere, Pinecone, or any hosted embedder.
Embeddings stay on the Ollama container; vectors stay in local Postgres.
