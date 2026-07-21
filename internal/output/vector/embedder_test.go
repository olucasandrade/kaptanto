package vector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEmbedder_Providers(t *testing.T) {
	t.Parallel()

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		Model:    "text-embedding-3-small",
		APIKey:   "sk-test",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-small", e.Model())
	assert.Equal(t, vector.DefaultEmbedderCap, e.Cap())

	e, err = vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		Model:    "embed-english-v3.0",
		APIKey:   "co-test",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "embed-english-v3.0", e.Model())
	assert.Equal(t, vector.DefaultEmbedderCap, e.Cap())

	_, err = vector.NewEmbedder(config.VectorEmbedderConfig{Provider: "huggingface", Model: "x"}, nil)
	require.Error(t, err)
	_, err = vector.NewEmbedder(config.VectorEmbedderConfig{Provider: "", Model: "x"}, nil)
	require.Error(t, err)
}

func TestEmbed_EmptyTexts(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		Model:    "text-embedding-3-small",
		BaseURL:  "http://127.0.0.1:1", // unused for empty batch
	}, nil)
	require.NoError(t, err)
	out, err := e.Embed(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestEmbed_ExceedsCap(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		Model:    "text-embedding-3-small",
		BaseURL:  "http://127.0.0.1:1",
	}, nil)
	require.NoError(t, err)
	texts := make([]string, vector.DefaultEmbedderCap+1)
	for i := range texts {
		texts[i] = "x"
	}
	_, err = e.Embed(context.Background(), texts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds cap")
}

// --- OpenAI golden httptest ---

func TestOpenAI_Embed_GoldenRequestAndOrder(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath, gotAuth, gotCT string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		// Return embeddings out of order to verify index sort (VEC-02).
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[
				{"object":"embedding","index":1,"embedding":[0.2,0.3]},
				{"object":"embedding","index":0,"embedding":[0.0,0.1]}
			],
			"model":"text-embedding-3-small",
			"usage":{"prompt_tokens":2,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider:   "openai",
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-secret",
		Model:      "text-embedding-3-small",
		Dimensions: 2,
	}, srv.Client())
	require.NoError(t, err)

	out, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1/embeddings", gotPath)
	assert.Equal(t, "Bearer sk-secret", gotAuth)
	assert.Equal(t, "application/json", gotCT)

	// Frozen request shape (OpenAI Create Embedding).
	var req map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &req))
	assert.JSONEq(t, `{
		"model":"text-embedding-3-small",
		"input":["alpha","beta"],
		"dimensions":2
	}`, string(gotBody))

	require.Len(t, out, 2)
	assert.Equal(t, []float32{0.0, 0.1}, out[0])
	assert.Equal(t, []float32{0.2, 0.3}, out[1])
}

func TestOpenAI_Embed_EmptyAPIKey_NoAuth(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1.0]}],"model":"nomic","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "", // local Ollama — no auth
		Model:    "nomic-embed-text",
	}, srv.Client())
	require.NoError(t, err)

	out, err := e.Embed(context.Background(), []string{"hello"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.False(t, sawAuth, "Authorization header must be absent when api-key is empty")
	assert.Empty(t, gotAuth)
}

func TestOpenAI_Embed_NoDimensionsOmitsField(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "sk-x",
		Model:    "text-embedding-3-small",
		// Dimensions unset
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"x"})
	require.NoError(t, err)
	assert.False(t, bytes.Contains(gotBody, []byte(`"dimensions"`)))
	assert.JSONEq(t, `{"model":"text-embedding-3-small","input":["x"]}`, string(gotBody))
}

func TestOpenAI_Embed_CountMismatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Only one vector for two texts — VEC-02 hard error.
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1.0]}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "sk-x",
		Model:    "m",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VEC-02")
	assert.Contains(t, err.Error(), "1 vectors for 2 texts")
}

func TestOpenAI_Embed_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "sk-x",
		Model:    "m",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

// --- Cohere golden httptest ---

func TestCohere_Embed_GoldenRequestAndOrder(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath, gotAuth, gotAccept string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		// embeddings.float order matches texts order per Cohere EmbedByTypeResponse.
		_, _ = w.Write([]byte(`{
			"id":"da6e531f-54c6-4a73-bf92-f60566d8d753",
			"embeddings":{
				"float":[
					[0.01,0.02],
					[0.03,0.04]
				]
			},
			"texts":["alpha","beta"]
		}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider:   "cohere",
		BaseURL:    srv.URL,
		APIKey:     "co-secret",
		Model:      "embed-english-v3.0",
		Dimensions: 1024,
	}, srv.Client())
	require.NoError(t, err)

	out, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v2/embed", gotPath)
	assert.Equal(t, "Bearer co-secret", gotAuth)
	assert.Equal(t, "application/json", gotAccept)

	assert.JSONEq(t, `{
		"model":"embed-english-v3.0",
		"texts":["alpha","beta"],
		"input_type":"search_document",
		"embedding_types":["float"],
		"output_dimension":1024
	}`, string(gotBody))

	require.Len(t, out, 2)
	assert.Equal(t, []float32{0.01, 0.02}, out[0])
	assert.Equal(t, []float32{0.03, 0.04}, out[1])
}

func TestCohere_Embed_EmptyAPIKey_NoAuth(t *testing.T) {
	t.Parallel()

	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","embeddings":{"float":[[1.0]]}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  srv.URL,
		APIKey:   "",
		Model:    "embed-english-v3.0",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"hello"})
	require.NoError(t, err)
	assert.False(t, sawAuth)
}

func TestCohere_Embed_NoDimensionsOmitsField(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","embeddings":{"float":[[0.5]]}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  srv.URL,
		APIKey:   "co-x",
		Model:    "embed-english-v3.0",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"x"})
	require.NoError(t, err)
	assert.False(t, bytes.Contains(gotBody, []byte(`"output_dimension"`)))
	assert.JSONEq(t, `{
		"model":"embed-english-v3.0",
		"texts":["x"],
		"input_type":"search_document",
		"embedding_types":["float"]
	}`, string(gotBody))
}

func TestCohere_Embed_CountMismatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","embeddings":{"float":[[1.0]]}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  srv.URL,
		APIKey:   "co-x",
		Model:    "embed-english-v3.0",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VEC-02")
}

func TestCohere_Embed_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad input"}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  srv.URL,
		APIKey:   "co-x",
		Model:    "embed-english-v3.0",
	}, srv.Client())
	require.NoError(t, err)

	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestCohere_Embed_Cap(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		Model:    "embed-english-v3.0",
		BaseURL:  "http://127.0.0.1:1",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, 96, e.Cap())
}

func TestNewEmbedder_EmptyModel(t *testing.T) {
	t.Parallel()
	_, err := vector.NewEmbedder(config.VectorEmbedderConfig{Provider: "openai", Model: "  "}, nil)
	require.Error(t, err)
	_, err = vector.NewEmbedder(config.VectorEmbedderConfig{Provider: "cohere", Model: ""}, nil)
	require.Error(t, err)
}

func TestNewEmbedder_DefaultBaseURLs(t *testing.T) {
	t.Parallel()
	// Defaults are applied; hit a dead host to prove URL construction without auth dance.
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		Model:    "text-embedding-3-small",
		APIKey:   "sk",
	}, &http.Client{Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"x"})
	require.Error(t, err)

	e, err = vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		Model:    "embed-english-v3.0",
		APIKey:   "co",
	}, &http.Client{Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"x"})
	require.Error(t, err)
}

func TestOpenAI_Embed_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai", BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestOpenAI_Embed_EmptyVector(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[]}]}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai", BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestOpenAI_Embed_MissingIndex(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Two items but indices 0 and 2 — gap at 1 after sort.
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[2]}]}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai", BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing embedding index")
}

func TestOpenAI_Embed_LongErrorBodyTruncated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("e", 400)))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai", BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "…")
}

func TestOpenAI_Embed_InvalidURL(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "openai",
		BaseURL:  "http://example.com/v1\x00bad",
		Model:    "m",
		APIKey:   "k",
	}, nil)
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
}

func TestCohere_Embed_EmptyTexts(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere", Model: "embed-english-v3.0", BaseURL: "http://127.0.0.1:1",
	}, nil)
	require.NoError(t, err)
	out, err := e.Embed(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestCohere_Embed_ExceedsCap(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere", Model: "embed-english-v3.0", BaseURL: "http://127.0.0.1:1",
	}, nil)
	require.NoError(t, err)
	texts := make([]string, vector.DefaultEmbedderCap+1)
	for i := range texts {
		texts[i] = "x"
	}
	_, err = e.Embed(context.Background(), texts)
	require.Error(t, err)
}

func TestCohere_Embed_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere", BaseURL: srv.URL, Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestCohere_Embed_EmptyVector(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":{"float":[[]]}}`))
	}))
	t.Cleanup(srv.Close)

	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere", BaseURL: srv.URL, Model: "m", APIKey: "k",
	}, srv.Client())
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestCohere_Embed_NetworkError(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  "http://127.0.0.1:1",
		Model:    "m",
		APIKey:   "k",
	}, &http.Client{Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
}

func TestCohere_Embed_InvalidURL(t *testing.T) {
	t.Parallel()
	e, err := vector.NewEmbedder(config.VectorEmbedderConfig{
		Provider: "cohere",
		BaseURL:  "http://example.com/\x00",
		Model:    "m",
		APIKey:   "k",
	}, nil)
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
}
