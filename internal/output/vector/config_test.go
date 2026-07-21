package vector_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func validBase() config.VectorSinkConfig {
	return config.VectorSinkConfig{
		Source: config.VectorSourceConfig{
			Columns: []string{"title", "body"},
		},
		Embedder: config.VectorEmbedderConfig{
			Provider: "openai",
			APIKey:   "${OPENAI_API_KEY}",
			Model:    "text-embedding-3-small",
		},
		Store: config.VectorStoreConfig{
			Provider: "pgvector",
			DSN:      "${PGVECTOR_DSN}",
		},
	}
}

func TestValidate_OK_Defaults(t *testing.T) {
	cfg := validBase()
	require.NoError(t, vector.Validate(&cfg))
	assert.Equal(t, vector.DefaultBatchMaxEvents, cfg.Batch.MaxEvents)
	assert.Equal(t, vector.DefaultPGVectorTable, cfg.Store.Table)
	assert.Equal(t, vector.DefaultOpenAIBaseURL, cfg.Embedder.BaseURL)
}

func TestValidate_SourceMatrix(t *testing.T) {
	t.Run("both columns and template", func(t *testing.T) {
		cfg := validBase()
		cfg.Source.Template = "{{.Table}}"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both")
	})
	t.Run("neither columns nor template", func(t *testing.T) {
		cfg := validBase()
		cfg.Source.Columns = nil
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither")
	})
	t.Run("template ok", func(t *testing.T) {
		cfg := validBase()
		cfg.Source.Columns = nil
		cfg.Source.Template = "{{.Schema}}.{{.Table}}: {{.Operation}}"
		require.NoError(t, vector.Validate(&cfg))
	})
	t.Run("template parse error", func(t *testing.T) {
		cfg := validBase()
		cfg.Source.Columns = nil
		cfg.Source.Template = "{{.Table"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "template parse")
	})
}

func TestValidate_EmbedderMatrix(t *testing.T) {
	t.Run("missing provider", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.Provider = ""
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider")
	})
	t.Run("bad provider", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.Provider = "huggingface"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})
	t.Run("missing model", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.Model = ""
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model")
	})
	t.Run("literal api-key rejected", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.APIKey = "sk-literal-secret"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "secret")
		assert.Contains(t, err.Error(), "${VAR}")
	})
	t.Run("empty api-key allowed", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.APIKey = ""
		cfg.Embedder.BaseURL = "http://localhost:11434/v1"
		require.NoError(t, vector.Validate(&cfg))
	})
	t.Run("cohere ok", func(t *testing.T) {
		cfg := validBase()
		cfg.Embedder.Provider = "cohere"
		cfg.Embedder.Model = "embed-english-v3.0"
		require.NoError(t, vector.Validate(&cfg))
	})
}

func TestValidate_StoreMatrix(t *testing.T) {
	t.Run("missing provider", func(t *testing.T) {
		cfg := validBase()
		cfg.Store.Provider = ""
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider")
	})
	t.Run("bad provider", func(t *testing.T) {
		cfg := validBase()
		cfg.Store.Provider = "weaviate"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})
	t.Run("pgvector literal dsn rejected", func(t *testing.T) {
		cfg := validBase()
		cfg.Store.DSN = "postgres://user:pass@localhost/db"
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store.dsn")
	})
	t.Run("pgvector missing dsn", func(t *testing.T) {
		cfg := validBase()
		cfg.Store.DSN = ""
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store.dsn")
	})
	t.Run("pinecone requires api-key and index-host", func(t *testing.T) {
		cfg := validBase()
		cfg.Store = config.VectorStoreConfig{Provider: "pinecone"}
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store.api-key")

		cfg.Store.APIKey = "${PINECONE_API_KEY}"
		err = vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "index-host")

		cfg.Store.IndexHost = "my-index.svc.pinecone.io"
		require.NoError(t, vector.Validate(&cfg))
	})
	t.Run("pinecone literal api-key rejected", func(t *testing.T) {
		cfg := validBase()
		cfg.Store = config.VectorStoreConfig{
			Provider:  "pinecone",
			APIKey:    "pcsk_literal",
			IndexHost: "h.svc.pinecone.io",
		}
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "secret")
	})
	t.Run("qdrant requires url and collection", func(t *testing.T) {
		cfg := validBase()
		cfg.Store = config.VectorStoreConfig{Provider: "qdrant"}
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "url")

		cfg.Store.URL = "http://localhost:6333"
		err = vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "collection")

		cfg.Store.Collection = "docs"
		require.NoError(t, vector.Validate(&cfg))
	})
	t.Run("qdrant optional api-key must be ref when set", func(t *testing.T) {
		cfg := validBase()
		cfg.Store = config.VectorStoreConfig{
			Provider:   "qdrant",
			URL:        "http://localhost:6333",
			Collection: "docs",
			APIKey:     "literal-key",
		}
		err := vector.Validate(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store.api-key")

		cfg.Store.APIKey = "${QDRANT_API_KEY}"
		require.NoError(t, vector.Validate(&cfg))
	})
}

func TestValidate_NilConfig(t *testing.T) {
	err := vector.Validate(nil)
	require.Error(t, err)
}

func TestSinks_Vector_YAMLRoundTrip(t *testing.T) {
	raw := `
sinks:
  vector:
    source:
      columns: [title, body]
    embedder:
      provider: openai
      api-key: ${OPENAI_API_KEY}
      model: text-embedding-3-small
      dimensions: 1536
    store:
      provider: pgvector
      dsn: ${PGVECTOR_DSN}
      table: my_vectors
    metadata: [author, category]
    batch:
      max-events: 32
`
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))
	require.NotNil(t, cfg.Sinks.Vector)
	v := cfg.Sinks.Vector
	assert.Equal(t, []string{"title", "body"}, v.Source.Columns)
	assert.Equal(t, "openai", v.Embedder.Provider)
	assert.Equal(t, "${OPENAI_API_KEY}", v.Embedder.APIKey)
	assert.Equal(t, "text-embedding-3-small", v.Embedder.Model)
	assert.Equal(t, 1536, v.Embedder.Dimensions)
	assert.Equal(t, "pgvector", v.Store.Provider)
	assert.Equal(t, "${PGVECTOR_DSN}", v.Store.DSN)
	assert.Equal(t, "my_vectors", v.Store.Table)
	assert.Equal(t, []string{"author", "category"}, v.Metadata)
	assert.Equal(t, 32, v.Batch.MaxEvents)
}

func TestSinks_Vector_AbsentIsNil(t *testing.T) {
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal([]byte(`output: stdout`), &cfg))
	assert.Nil(t, cfg.Sinks.Vector)
}
