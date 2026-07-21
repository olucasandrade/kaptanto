package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/pk"
)

// VectorStore upserts/deletes vectors. Batch calls; order within a call is
// preserved by construction (one HTTP request / one pgx batch).
type VectorStore interface {
	Upsert(ctx context.Context, recs []Record) error
	Delete(ctx context.Context, ids []string) error
	Ping(ctx context.Context) error
	Close() error
}

// Record is one vector to upsert. ID follows VEC-03.
type Record struct {
	ID       string         // VEC-03: "<schema.table>:<canonical-key-JSON>"
	Vector   []float32
	Metadata map[string]any // table, operation, timestamp, configured metadata columns
	Text     string         // stored when the store supports it
}

// CanonicalID builds the VEC-03 stable vector identity:
// "<schema.table>:<canonical-key-JSON>" with sorted key fields via pk.Canonical.
// Empty schema yields "<table>:<canonical-key-JSON>".
func CanonicalID(schema, table string, key map[string]any) (string, error) {
	table = strings.TrimSpace(table)
	if table == "" {
		return "", fmt.Errorf("vector: CanonicalID: table is required")
	}
	if key == nil {
		key = map[string]any{}
	}
	canon, err := pk.Canonical(key)
	if err != nil {
		return "", fmt.Errorf("vector: CanonicalID: %w", err)
	}
	name := table
	if s := strings.TrimSpace(schema); s != "" {
		name = s + "." + table
	}
	return name + ":" + string(canon), nil
}

// CanonicalIDFromRaw unmarshals key JSON then builds CanonicalID (VEC-03).
func CanonicalIDFromRaw(schema, table string, key json.RawMessage) (string, error) {
	var m map[string]any
	if len(key) > 0 && string(key) != "null" {
		if err := json.Unmarshal(key, &m); err != nil {
			return "", fmt.Errorf("vector: CanonicalID: key: %w", err)
		}
	}
	return CanonicalID(schema, table, m)
}

// OpenStore constructs a VectorStore for cfg.Provider.
// Secrets (DSN, API keys) must already be expanded from ${VAR} references.
// dimensions is the embedding width (required for pgvector CREATE TABLE and
// Qdrant collection auto-create); ignored by Pinecone (index already sized).
func OpenStore(ctx context.Context, cfg config.VectorStoreConfig, dimensions int) (VectorStore, error) {
	switch cfg.Provider {
	case "pgvector":
		return OpenPGVector(ctx, cfg.DSN, cfg.Table, dimensions)
	case "pinecone":
		return OpenPinecone(cfg.APIKey, cfg.IndexHost, cfg.Namespace)
	case "qdrant":
		return OpenQdrant(ctx, cfg.URL, cfg.APIKey, cfg.Collection, dimensions)
	case "":
		return nil, fmt.Errorf("vector: store: provider is required (pgvector|pinecone|qdrant)")
	default:
		return nil, fmt.Errorf("vector: store: unknown provider %q (pgvector|pinecone|qdrant)", cfg.Provider)
	}
}
