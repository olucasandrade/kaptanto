package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// DefaultCohereBaseURL is Cohere's production API host (Embed v2).
const DefaultCohereBaseURL = "https://api.cohere.com"

// cohereEmbedder calls POST {baseURL}/v2/embed
// (https://docs.cohere.com/reference/embed).
type cohereEmbedder struct {
	client     *http.Client
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	cap        int
}

func newCohereEmbedder(cfg config.VectorEmbedderConfig, client *http.Client) (*cohereEmbedder, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("vector: cohere embedder: model is required")
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultCohereBaseURL
	}
	return &cohereEmbedder{
		client:     client,
		baseURL:    strings.TrimRight(base, "/"),
		apiKey:     cfg.APIKey,
		model:      model,
		dimensions: cfg.Dimensions,
		cap:        DefaultEmbedderCap,
	}, nil
}

func (e *cohereEmbedder) Cap() int      { return e.cap }
func (e *cohereEmbedder) Model() string { return e.model }

// cohereRequest is the Embed v2 request body (texts path).
// input_type is required for Embed v3+; search_document is the CDC upsert path.
type cohereRequest struct {
	Model           string   `json:"model"`
	Texts           []string `json:"texts"`
	InputType       string   `json:"input_type"`
	EmbeddingTypes  []string `json:"embedding_types"`
	OutputDimension *int     `json:"output_dimension,omitempty"`
}

// cohereResponse is the EmbedByTypeResponse shape.
type cohereResponse struct {
	Embeddings cohereEmbeddings `json:"embeddings"`
}

type cohereEmbeddings struct {
	Float [][]float32 `json:"float"`
}

func (e *cohereEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if err := checkBatchSize(e.cap, texts); err != nil {
		return nil, err
	}

	reqBody := cohereRequest{
		Model:          e.model,
		Texts:          texts,
		InputType:      "search_document",
		EmbeddingTypes: []string{"float"},
	}
	if e.dimensions > 0 {
		d := e.dimensions
		reqBody.OutputDimension = &d
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vector: cohere embed: marshal: %w", err)
	}

	url := joinURL(e.baseURL, "v2/embed")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("vector: cohere embed: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(e.apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vector: cohere embed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("vector: cohere embed: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode, Msg: "cohere embed: " + truncateForErr(body)}
	}

	var parsed cohereResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vector: cohere embed: decode: %w", err)
	}
	if err := checkCountMatch(len(texts), len(parsed.Embeddings.Float)); err != nil {
		return nil, err
	}
	for i, vec := range parsed.Embeddings.Float {
		if len(vec) == 0 {
			return nil, fmt.Errorf("vector: cohere embed: empty embedding at index %d", i)
		}
	}
	return parsed.Embeddings.Float, nil
}
