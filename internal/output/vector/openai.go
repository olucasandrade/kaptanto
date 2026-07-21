package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// openAIEmbedder calls an OpenAI-compatible POST {baseURL}/embeddings endpoint.
// baseURL defaults to https://api.openai.com/v1 (Ollama/LM Studio/TEI/vLLM use
// the same /v1/embeddings shape via a custom base-url).
type openAIEmbedder struct {
	client     *http.Client
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	cap        int
}

func newOpenAIEmbedder(cfg config.VectorEmbedderConfig, client *http.Client) (*openAIEmbedder, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("vector: openai embedder: model is required")
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultOpenAIBaseURL
	}
	return &openAIEmbedder{
		client:     client,
		baseURL:    strings.TrimRight(base, "/"),
		apiKey:     cfg.APIKey,
		model:      model,
		dimensions: cfg.Dimensions,
		cap:        DefaultEmbedderCap,
	}, nil
}

func (e *openAIEmbedder) Cap() int      { return e.cap }
func (e *openAIEmbedder) Model() string { return e.model }

// openAIRequest is the Create Embedding request body
// (https://platform.openai.com/docs/api-reference/embeddings/create).
type openAIRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions *int     `json:"dimensions,omitempty"`
}

// openAIResponse is the Create Embedding response body.
type openAIResponse struct {
	Data []openAIEmbedding `json:"data"`
}

type openAIEmbedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if err := checkBatchSize(e.cap, texts); err != nil {
		return nil, err
	}

	reqBody := openAIRequest{Model: e.model, Input: texts}
	if e.dimensions > 0 {
		d := e.dimensions
		reqBody.Dimensions = &d
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vector: openai embed: marshal: %w", err)
	}

	url := joinURL(e.baseURL, "embeddings")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("vector: openai embed: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Bearer only when api-key is non-empty (local Ollama etc. need no auth).
	if key := strings.TrimSpace(e.apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vector: openai embed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("vector: openai embed: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode, Msg: "openai embed: " + truncateForErr(body)}
	}

	var parsed openAIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vector: openai embed: decode: %w", err)
	}
	if err := checkCountMatch(len(texts), len(parsed.Data)); err != nil {
		return nil, err
	}

	// OpenAI may return data unsorted; reorder by index so out[i] == texts[i] (VEC-02).
	sort.Slice(parsed.Data, func(i, j int) bool {
		return parsed.Data[i].Index < parsed.Data[j].Index
	})
	out := make([][]float32, len(parsed.Data))
	for i, item := range parsed.Data {
		if item.Index != i {
			return nil, fmt.Errorf("vector: openai embed: missing embedding index %d (VEC-02)", i)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("vector: openai embed: empty embedding at index %d", i)
		}
		out[i] = item.Embedding
	}
	return out, nil
}

func truncateForErr(b []byte) string {
	const max = 256
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
