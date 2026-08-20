-- Similarity search against vectors written by the Kaptanto vector sink.
-- Run after compose is up and seed/backfill has embedded the documents.
--
-- Requires the nomic-embed-text query embedding for the search phrase.
-- Prefer the helper in README (curl Ollama + psql). This file shows the SQL shape.

-- Example: replace :query_embedding with a 768-dim vector literal from Ollama.
-- SELECT id, text, metadata, 1 - ("vector" <=> :query_embedding) AS score
-- FROM kaptanto_vectors
-- ORDER BY "vector" <=> :query_embedding
-- LIMIT 5;

SELECT id, left(text, 80) AS preview, metadata
FROM kaptanto_vectors
ORDER BY id;
