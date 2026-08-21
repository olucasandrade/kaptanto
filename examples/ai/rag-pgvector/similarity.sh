#!/usr/bin/env bash
# Embed a query with local Ollama and run cosine similarity against pgvector.
# Usage: ./similarity.sh "local RAG with vectors"
set -euo pipefail

QUERY="${1:-local RAG with vectors}"
OLLAMA_URL="${OLLAMA_URL:-http://localhost:11434}"
PGURL="${PGURL:-postgres://postgres:postgres@localhost:5441/rag?sslmode=disable}"

EMBED_JSON=$(curl -sf "${OLLAMA_URL}/api/embeddings" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg model nomic-embed-text --arg prompt "$QUERY" \
        '{model:$model, prompt:$prompt}')")

VEC=$(echo "$EMBED_JSON" | jq -c '.embedding')
if [[ "$VEC" == "null" || -z "$VEC" ]]; then
  echo "failed to embed query via Ollama at ${OLLAMA_URL}" >&2
  exit 1
fi

# pgvector accepts '[1,2,3]' literals; jq -c already emits JSON arrays.
psql "$PGURL" -v ON_ERROR_STOP=1 -c "
SELECT
  id,
  left(text, 100) AS preview,
  metadata,
  1 - (\"vector\" <=> '${VEC}'::vector) AS score
FROM kaptanto_vectors
ORDER BY \"vector\" <=> '${VEC}'::vector
LIMIT 5;
"
