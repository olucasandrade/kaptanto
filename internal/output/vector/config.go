// Package vector implements the vector store sink: config validation, text
// extraction, the VEC-01 SHA-256 hash cache, OpenAI-compatible / Cohere
// embedders, VectorStore backends (pgvector, Pinecone, Qdrant), and the
// router consumer.
package vector

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// DefaultBatchMaxEvents is applied when batch.max-events is unset or ≤0.
const DefaultBatchMaxEvents = 96

// DefaultPGVectorTable is the default pgvector table name.
const DefaultPGVectorTable = "kaptanto_vectors"

// DefaultOpenAIBaseURL is the default OpenAI-compatible embeddings endpoint.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

// envRefRegex validates STRICT ${VAR} secret references (ACT-02 / vector secrets).
var envRefRegex = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Validate checks cfg and applies defaults (batch.max-events, store.table,
// embedder.base-url). It does not resolve ${VAR} secrets — callers expand
// them at sink construction time.
//
// Secret fields (api-key, dsn) must be exactly a ${VAR} reference when set;
// a literal value is a startup error.
func Validate(cfg *config.VectorSinkConfig) error {
	if cfg == nil {
		return fmt.Errorf("vector: config is required")
	}

	if err := validateSource(&cfg.Source); err != nil {
		return err
	}
	if err := validateEmbedder(&cfg.Embedder); err != nil {
		return err
	}
	if err := validateStore(&cfg.Store); err != nil {
		return err
	}

	if cfg.Batch.MaxEvents <= 0 {
		cfg.Batch.MaxEvents = DefaultBatchMaxEvents
	}
	return nil
}

func validateSource(src *config.VectorSourceConfig) error {
	hasCols := len(src.Columns) > 0
	hasTmpl := strings.TrimSpace(src.Template) != ""
	switch {
	case hasCols && hasTmpl:
		return fmt.Errorf("vector: source: exactly one of columns or template must be set, got both")
	case !hasCols && !hasTmpl:
		return fmt.Errorf("vector: source: exactly one of columns or template must be set, got neither")
	case hasTmpl:
		if _, err := template.New("vector-source").Option("missingkey=error").Parse(src.Template); err != nil {
			return fmt.Errorf("vector: source: template parse: %w", err)
		}
	}
	return nil
}

func validateEmbedder(e *config.VectorEmbedderConfig) error {
	switch e.Provider {
	case "openai", "cohere":
	case "":
		return fmt.Errorf("vector: embedder: provider is required (openai|cohere)")
	default:
		return fmt.Errorf("vector: embedder: unknown provider %q (openai|cohere)", e.Provider)
	}
	if strings.TrimSpace(e.Model) == "" {
		return fmt.Errorf("vector: embedder: model is required")
	}
	if err := requireSecretRef("embedder.api-key", e.APIKey, true); err != nil {
		return err
	}
	if e.Provider == "openai" && strings.TrimSpace(e.BaseURL) == "" {
		e.BaseURL = DefaultOpenAIBaseURL
	}
	return nil
}

func validateStore(s *config.VectorStoreConfig) error {
	switch s.Provider {
	case "pgvector":
		if err := requireSecretRef("store.dsn", s.DSN, false); err != nil {
			return err
		}
		if strings.TrimSpace(s.Table) == "" {
			s.Table = DefaultPGVectorTable
		}
	case "pinecone":
		if err := requireSecretRef("store.api-key", s.APIKey, false); err != nil {
			return err
		}
		if strings.TrimSpace(s.IndexHost) == "" {
			return fmt.Errorf("vector: store: pinecone requires index-host")
		}
	case "qdrant":
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("vector: store: qdrant requires url")
		}
		if strings.TrimSpace(s.Collection) == "" {
			return fmt.Errorf("vector: store: qdrant requires collection")
		}
		if err := requireSecretRef("store.api-key", s.APIKey, true); err != nil {
			return err
		}
	case "":
		return fmt.Errorf("vector: store: provider is required (pgvector|pinecone|qdrant)")
	default:
		return fmt.Errorf("vector: store: unknown provider %q (pgvector|pinecone|qdrant)", s.Provider)
	}
	return nil
}

// requireSecretRef enforces STRICT ${VAR} for secret fields.
// allowEmpty permits an empty value (e.g. local embedder without an API key).
func requireSecretRef(field, value string, allowEmpty bool) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("vector: %s is required and must be an environment variable reference like ${VAR}", field)
	}
	if !envRefRegex.MatchString(trimmed) {
		return fmt.Errorf("vector: %s is secret and must be an environment variable reference like ${VAR}", field)
	}
	return nil
}
