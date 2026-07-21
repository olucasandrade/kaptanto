package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Pinecone API version frozen against docs.pinecone.io (2025-10 data plane).
const pineconeAPIVersion = "2025-10"

// PineconeStore upserts/deletes via the Pinecone data-plane HTTP API.
type PineconeStore struct {
	apiKey    string
	baseURL   string // https://{index-host}
	namespace string
	client    *http.Client
}

// OpenPinecone builds a PineconeStore. indexHost may be a bare host or a full URL.
func OpenPinecone(apiKey, indexHost, namespace string) (*PineconeStore, error) {
	apiKey = strings.TrimSpace(apiKey)
	indexHost = strings.TrimSpace(indexHost)
	if apiKey == "" {
		return nil, fmt.Errorf("vector: pinecone: api-key is required")
	}
	if indexHost == "" {
		return nil, fmt.Errorf("vector: pinecone: index-host is required")
	}
	base := indexHost
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	return &PineconeStore{
		apiKey:    apiKey,
		baseURL:   base,
		namespace: namespace,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

type pineconeUpsertRequest struct {
	Vectors   []pineconeVector `json:"vectors"`
	Namespace string           `json:"namespace,omitempty"`
}

type pineconeVector struct {
	ID       string         `json:"id"`
	Values   []float32      `json:"values"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type pineconeDeleteRequest struct {
	IDs       []string `json:"ids"`
	Namespace string   `json:"namespace,omitempty"`
}

// Upsert sends one POST /vectors/upsert (order preserved by construction).
func (s *PineconeStore) Upsert(ctx context.Context, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	body := pineconeUpsertRequest{
		Vectors:   make([]pineconeVector, len(recs)),
		Namespace: s.namespace,
	}
	for i, rec := range recs {
		meta := cloneMetadata(rec.Metadata)
		if rec.Text != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["text"] = rec.Text
		}
		body.Vectors[i] = pineconeVector{
			ID:       rec.ID,
			Values:   rec.Vector,
			Metadata: meta,
		}
	}
	return s.doJSON(ctx, http.MethodPost, s.baseURL+"/vectors/upsert", body)
}

// Delete sends one POST /vectors/delete.
func (s *PineconeStore) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	body := pineconeDeleteRequest{IDs: ids, Namespace: s.namespace}
	return s.doJSON(ctx, http.MethodPost, s.baseURL+"/vectors/delete", body)
}

// Ping calls POST /describe_index_stats.
func (s *PineconeStore) Ping(ctx context.Context) error {
	return s.doJSON(ctx, http.MethodPost, s.baseURL+"/describe_index_stats", map[string]any{})
}

// Close is a no-op for the HTTP client.
func (s *PineconeStore) Close() error { return nil }

func (s *PineconeStore) doJSON(ctx context.Context, method, url string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("vector: pinecone: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("vector: pinecone: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", s.apiKey)
	req.Header.Set("X-Pinecone-Api-Version", pineconeAPIVersion)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("vector: pinecone: %s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vector: pinecone: %s %s: status %d: %s", method, url, resp.StatusCode, truncate(string(body), 512))
	}
	return nil
}

func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
