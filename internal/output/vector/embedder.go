package vector

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// DefaultEmbedderCap is the per-provider batch size (decision #32; Cohere
// hard-max is 96). Consumers clamp FlushBatch chunks to min(batch.max-events, Cap()).
const DefaultEmbedderCap = 96

// defaultHTTPTimeout bounds a single embed HTTP round-trip.
const defaultHTTPTimeout = 60 * time.Second

// Embedder turns texts into vectors. Batch size must be ≤ Cap().
// Order-preserving: out[i] embeds texts[i] (VEC-02).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Cap() int
	Model() string
}

// NewEmbedder builds an Embedder from validated config. apiKey must already be
// expanded from ${VAR} (or empty for local OpenAI-compatible endpoints).
// client may be nil (a default client with Timeout is used).
func NewEmbedder(cfg config.VectorEmbedderConfig, client *http.Client) (Embedder, error) {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "openai":
		return newOpenAIEmbedder(cfg, client)
	case "cohere":
		return newCohereEmbedder(cfg, client)
	case "":
		return nil, fmt.Errorf("vector: embedder: provider is required (openai|cohere)")
	default:
		return nil, fmt.Errorf("vector: embedder: unknown provider %q (openai|cohere)", cfg.Provider)
	}
}

// checkBatchSize enforces Cap() before any HTTP call.
func checkBatchSize(cap int, texts []string) error {
	if len(texts) > cap {
		return fmt.Errorf("vector: embed batch size %d exceeds cap %d", len(texts), cap)
	}
	return nil
}

// checkCountMatch enforces VEC-02: never zip misaligned vectors/texts.
// Fewer (or more) vectors than texts is a hard error — treated as transient
// by the consumer so the partition retries rather than DLQing a whole chunk.
func checkCountMatch(texts int, vectors int) error {
	if vectors != texts {
		return fmt.Errorf("vector: embedder returned %d vectors for %d texts (VEC-02)", vectors, texts)
	}
	return nil
}

// joinURL concatenates base and path, trimming a single trailing/leading slash.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
