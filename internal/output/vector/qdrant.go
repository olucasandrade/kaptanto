package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// qdrantIDNamespace is a fixed UUID namespace for deterministic point IDs.
// Qdrant only accepts uint64 or UUID point IDs; VEC-03 string IDs are mapped
// via UUID v5 and the original id is stored in the payload.
var qdrantIDNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// QdrantStore upserts/deletes via the Qdrant HTTP API.
type QdrantStore struct {
	baseURL    string
	apiKey     string
	collection string
	dimensions int
	client     *http.Client
}

// OpenQdrant builds a QdrantStore and ensures the collection exists
// (auto-create with the given dimensions; concurrent creates are idempotent).
func OpenQdrant(ctx context.Context, baseURL, apiKey, collection string, dimensions int) (*QdrantStore, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	collection = strings.TrimSpace(collection)
	if baseURL == "" {
		return nil, fmt.Errorf("vector: qdrant: url is required")
	}
	if collection == "" {
		return nil, fmt.Errorf("vector: qdrant: collection is required")
	}
	if dimensions <= 0 {
		return nil, fmt.Errorf("vector: qdrant: dimensions must be > 0")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("vector: qdrant: url: %w", err)
	}
	s := &QdrantStore{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(apiKey),
		collection: collection,
		dimensions: dimensions,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	if err := s.ensureCollection(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *QdrantStore) ensureCollection(ctx context.Context) error {
	path := s.baseURL + "/collections/" + url.PathEscape(s.collection)
	status, body, err := s.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("vector: qdrant: get collection: %w", err)
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("vector: qdrant: get collection: status %d: %s", status, truncate(string(body)))
	}
	create := map[string]any{
		"vectors": map[string]any{
			"size":     s.dimensions,
			"distance": "Cosine",
		},
	}
	status, body, err = s.doRaw(ctx, http.MethodPut, path, create)
	if err != nil {
		return fmt.Errorf("vector: qdrant: create collection: %w", err)
	}
	// 200 = created; 409 = another node won the race — both OK.
	if status == http.StatusOK || status == http.StatusCreated || status == http.StatusConflict {
		return nil
	}
	// Some Qdrant versions return 400 "already exists".
	if status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "already") {
		return nil
	}
	return fmt.Errorf("vector: qdrant: create collection: status %d: %s", status, truncate(string(body)))
}

type qdrantPoint struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

type qdrantUpsertRequest struct {
	Points []qdrantPoint `json:"points"`
}

type qdrantDeleteRequest struct {
	Points []string `json:"points"`
}

// Upsert sends one PUT …/points?wait=true (order preserved by construction).
func (s *QdrantStore) Upsert(ctx context.Context, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	body := qdrantUpsertRequest{Points: make([]qdrantPoint, len(recs))}
	for i, rec := range recs {
		payload := cloneMetadata(rec.Metadata)
		if payload == nil {
			payload = map[string]any{}
		}
		payload["id"] = rec.ID
		if rec.Text != "" {
			payload["text"] = rec.Text
		}
		body.Points[i] = qdrantPoint{
			ID:      qdrantPointID(rec.ID),
			Vector:  rec.Vector,
			Payload: payload,
		}
	}
	path := fmt.Sprintf("%s/collections/%s/points?wait=true", s.baseURL, url.PathEscape(s.collection))
	return s.doJSON(ctx, http.MethodPut, path, body)
}

// Delete sends one POST …/points/delete?wait=true.
func (s *QdrantStore) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	points := make([]string, len(ids))
	for i, id := range ids {
		points[i] = qdrantPointID(id)
	}
	path := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", s.baseURL, url.PathEscape(s.collection))
	return s.doJSON(ctx, http.MethodPost, path, qdrantDeleteRequest{Points: points})
}

// Ping GETs the collection.
func (s *QdrantStore) Ping(ctx context.Context) error {
	path := s.baseURL + "/collections/" + url.PathEscape(s.collection)
	status, body, err := s.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("vector: qdrant: ping: %w", err)
	}
	if status < 200 || status >= 300 {
		return &StatusError{Status: status, Msg: "qdrant: ping: " + truncate(string(body))}
	}
	return nil
}

// Close is a no-op for the HTTP client.
func (s *QdrantStore) Close() error { return nil }

// qdrantPointID maps a VEC-03 string ID to a deterministic UUID (Qdrant constraint).
func qdrantPointID(canonicalID string) string {
	return uuid.NewSHA1(qdrantIDNamespace, []byte(canonicalID)).String()
}

func (s *QdrantStore) doJSON(ctx context.Context, method, rawURL string, payload any) error {
	status, body, err := s.doRaw(ctx, method, rawURL, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &StatusError{
			Status: status,
			Msg:    fmt.Sprintf("qdrant: %s %s: %s", method, rawURL, truncate(string(body))),
		}
	}
	return nil
}

func (s *QdrantStore) doRaw(ctx context.Context, method, rawURL string, payload any) (int, []byte, error) {
	var rdr io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("vector: qdrant: marshal: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("vector: qdrant: request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}
