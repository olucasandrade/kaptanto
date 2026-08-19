package vector_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinecone_UpsertGoldenRequest(t *testing.T) {
	var gotMethod, gotPath, gotAPIKey, gotVersion, gotCT string
	var gotBody []byte
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("Api-Key")
		gotVersion = r.Header.Get("X-Pinecone-Api-Version")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upsertedCount":2}`))
	}))
	t.Cleanup(srv.Close)

	store, err := vector.OpenPinecone("test-key", srv.URL, "ns1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	recs := []vector.Record{
		{ID: `public.orders:{"id":"1"}`, Vector: []float32{0.1, 0.2}, Text: "hello", Metadata: map[string]any{"op": "insert"}},
		{ID: `public.orders:{"id":"2"}`, Vector: []float32{0.3, 0.4}, Metadata: map[string]any{"op": "update"}},
	}
	require.NoError(t, store.Upsert(context.Background(), recs))

	assert.Equal(t, int32(1), calls.Load(), "order preserved by construction: single HTTP request")
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/vectors/upsert", gotPath)
	assert.Equal(t, "test-key", gotAPIKey)
	assert.Equal(t, "2025-10", gotVersion)
	assert.Equal(t, "application/json", gotCT)

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "ns1", body["namespace"])

	vectors, ok := body["vectors"].([]any)
	require.True(t, ok)
	require.Len(t, vectors, 2)

	v0 := vectors[0].(map[string]any)
	assert.Equal(t, `public.orders:{"id":"1"}`, v0["id"])
	vals0 := toFloatSlice(t, v0["values"])
	assert.InDeltaSlice(t, []float64{0.1, 0.2}, vals0, 1e-9)
	meta0 := v0["metadata"].(map[string]any)
	assert.Equal(t, "insert", meta0["op"])
	assert.Equal(t, "hello", meta0["text"])

	v1 := vectors[1].(map[string]any)
	assert.Equal(t, `public.orders:{"id":"2"}`, v1["id"])
	vals1 := toFloatSlice(t, v1["values"])
	assert.InDeltaSlice(t, []float64{0.3, 0.4}, vals1, 1e-9)
}

func TestPinecone_DeleteGoldenRequest(t *testing.T) {
	var gotBody []byte
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/vectors/delete", r.URL.Path)
		assert.Equal(t, "k", r.Header.Get("Api-Key"))
		assert.Equal(t, "2025-10", r.Header.Get("X-Pinecone-Api-Version"))
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	store, err := vector.OpenPinecone("k", srv.URL, "ns")
	require.NoError(t, err)

	ids := []string{`public.orders:{"id":"1"}`, `public.orders:{"id":"2"}`}
	require.NoError(t, store.Delete(context.Background(), ids))
	assert.Equal(t, int32(1), calls.Load())

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "ns", body["namespace"])
	gotIDs := body["ids"].([]any)
	require.Len(t, gotIDs, 2)
	assert.Equal(t, ids[0], gotIDs[0])
	assert.Equal(t, ids[1], gotIDs[1])
}

func TestPinecone_PingAndEmptyOps(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	store, err := vector.OpenPinecone("k", srv.URL, "")
	require.NoError(t, err)
	require.NoError(t, store.Ping(context.Background()))
	require.NoError(t, store.Upsert(context.Background(), nil))
	require.NoError(t, store.Delete(context.Background(), nil))
	assert.Equal(t, []string{"/describe_index_stats"}, paths)
}

func TestPinecone_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad"}`))
	}))
	t.Cleanup(srv.Close)
	store, err := vector.OpenPinecone("k", srv.URL, "")
	require.NoError(t, err)
	err = store.Upsert(context.Background(), []vector.Record{{ID: "x", Vector: []float32{1}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestPinecone_OpenValidation(t *testing.T) {
	_, err := vector.OpenPinecone("", "host", "")
	require.Error(t, err)
	_, err = vector.OpenPinecone("k", "", "")
	require.Error(t, err)

	// Bare host gets https:// prefix.
	store, err := vector.OpenPinecone("k", "example.pinecone.io", "ns")
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestPinecone_ErrorRedactsURLAndOmitsBody(t *testing.T) {
	const secret = "s3cret"
	const queryKey = "supersecret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 600)))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	u.User = url.UserPassword("user", secret)
	q := u.Query()
	q.Set("api_key", queryKey)
	u.RawQuery = q.Encode()

	store, err := vector.OpenPinecone("k", u.String(), "")
	require.NoError(t, err)
	err = store.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), queryKey)
	assert.NotContains(t, err.Error(), strings.Repeat("x", 20))
	assert.NotContains(t, err.Error(), "…")

	rr := httptest.NewRecorder()
	observability.NewHealthHandler([]observability.HealthProbe{
		{Name: "vector-store", Check: func() error { return err }},
	}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.NotContains(t, rr.Body.String(), secret)
	assert.NotContains(t, rr.Body.String(), queryKey)
}

func TestOpenStore_Pinecone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	store, err := vector.OpenStore(context.Background(), config.VectorStoreConfig{
		Provider:  "pinecone",
		APIKey:    "k",
		IndexHost: srv.URL,
	}, 0)
	require.NoError(t, err)
	require.NoError(t, store.Ping(context.Background()))
	require.NoError(t, store.Close())
}

func toFloatSlice(t *testing.T, v any) []float64 {
	t.Helper()
	arr, ok := v.([]any)
	require.True(t, ok)
	out := make([]float64, len(arr))
	for i, x := range arr {
		f, ok := x.(float64)
		require.True(t, ok)
		out[i] = f
	}
	return out
}
