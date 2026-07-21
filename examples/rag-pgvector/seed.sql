CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id          SERIAL PRIMARY KEY,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL,
    category    TEXT NOT NULL DEFAULT 'general',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO documents (title, body, category) VALUES
    ('Getting started with CDC',
     'Change Data Capture streams row-level inserts, updates, and deletes from Postgres into downstream systems in near real time.',
     'docs'),
    ('Local RAG with pgvector',
     'Embed document text with a local Ollama model and store vectors in Postgres using the pgvector extension for cosine similarity search.',
     'rag'),
    ('Kaptanto vector sink',
     'Kaptanto extracts text from CDC events, embeds it via an OpenAI-compatible endpoint, and upserts vectors into pgvector, Pinecone, or Qdrant.',
     'product');
