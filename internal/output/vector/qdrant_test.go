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
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQdrant_UpsertGoldenRequest(t *testing.T) {
	var upsertBody []byte
	var upsertCalls atomic.Int32
	var createCalls atomic.Int32
	collections := map[string]bool{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret", r.Header.Get("api-key"))
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/"):
			name := strings.TrimPrefix(r.URL.Path, "/collections/")
			if collections[name] {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":{"status":"green"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":{"error":"Not found"}}`))
		case r.Method == http.MethodPut && !strings.Contains(r.URL.Path, "/points"):
			createCalls.Add(1)
			name := strings.TrimPrefix(r.URL.Path, "/collections/")
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &body))
			vecs := body["vectors"].(map[string]any)
			assert.Equal(t, float64(3), vecs["size"])
			assert.Equal(t, "Cosine", vecs["distance"])
			collections[name] = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/points"):
			upsertCalls.Add(1)
			assert.Equal(t, "true", r.URL.Query().Get("wait"))
			upsertBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	store, err := vector.OpenQdrant(context.Background(), srv.URL, "secret", "kaptanto", 3)
	require.NoError(t, err)
	assert.Equal(t, int32(1), createCalls.Load())

	recs := []vector.Record{
		{ID: `public.orders:{"id":"1"}`, Vector: []float32{0.1, 0.2, 0.3}, Text: "a", Metadata: map[string]any{"op": "insert"}},
		{ID: `public.orders:{"id":"2"}`, Vector: []float32{0.4, 0.5, 0.6}, Metadata: map[string]any{"op": "update"}},
	}
	require.NoError(t, store.Upsert(context.Background(), recs))
	assert.Equal(t, int32(1), upsertCalls.Load(), "order preserved by construction: single HTTP request")

	var body map[string]any
	require.NoError(t, json.Unmarshal(upsertBody, &body))
	points := body["points"].([]any)
	require.Len(t, points, 2)

	p0 := points[0].(map[string]any)
	p1 := points[1].(map[string]any)
	assert.NotEqual(t, recs[0].ID, p0["id"], "Qdrant uses UUID point IDs")
	assert.Regexp(t, `^[0-9a-f-]{36}$`, p0["id"])
	assert.InDeltaSlice(t, []float64{0.1, 0.2, 0.3}, toFloatSlice(t, p0["vector"]), 1e-9)
	payload0 := p0["payload"].(map[string]any)
	assert.Equal(t, recs[0].ID, payload0["id"])
	assert.Equal(t, "a", payload0["text"])
	assert.Equal(t, "insert", payload0["op"])

	assert.InDeltaSlice(t, []float64{0.4, 0.5, 0.6}, toFloatSlice(t, p1["vector"]), 1e-9)
	assert.Equal(t, recs[1].ID, p1["payload"].(map[string]any)["id"])
	require.NoError(t, store.Close())
}

func TestQdrant_DeleteGoldenRequest(t *testing.T) {
	collections := map[string]bool{"c": true}
	var deleteBody []byte
	var deleteCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/delete"):
			deleteCalls.Add(1)
			assert.Equal(t, "true", r.URL.Query().Get("wait"))
			deleteBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.String())
		}
		_ = collections
	}))
	t.Cleanup(srv.Close)

	store, err := vector.OpenQdrant(context.Background(), srv.URL, "", "c", 2)
	require.NoError(t, err)

	ids := []string{`public.orders:{"id":"1"}`, `public.orders:{"id":"2"}`}
	require.NoError(t, store.Delete(context.Background(), ids))
	assert.Equal(t, int32(1), deleteCalls.Load())

	var body map[string]any
	require.NoError(t, json.Unmarshal(deleteBody, &body))
	pts := body["points"].([]any)
	require.Len(t, pts, 2)
	assert.Regexp(t, `^[0-9a-f-]{36}$`, pts[0])
	assert.NotEqual(t, pts[0], pts[1])
}

func TestQdrant_CollectionIdempotentCreate(t *testing.T) {
	var creates atomic.Int32
	exists := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if exists {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut {
			creates.Add(1)
			exists = true
			w.WriteHeader(http.StatusConflict) // race winner already created
			_, _ = w.Write([]byte(`{"status":{"error":"already exists"}}`))
			return
		}
		t.Fatalf("unexpected %s", r.Method)
	}))
	t.Cleanup(srv.Close)

	_, err := vector.OpenQdrant(context.Background(), srv.URL, "", "c", 4)
	require.NoError(t, err)
	assert.Equal(t, int32(1), creates.Load())

	// Second open: collection exists → no create.
	_, err = vector.OpenQdrant(context.Background(), srv.URL, "", "c", 4)
	require.NoError(t, err)
	assert.Equal(t, int32(1), creates.Load())
}

func TestQdrant_OpenValidation(t *testing.T) {
	_, err := vector.OpenQdrant(context.Background(), "", "", "c", 1)
	require.Error(t, err)
	_, err = vector.OpenQdrant(context.Background(), "http://localhost", "", "", 1)
	require.Error(t, err)
	_, err = vector.OpenQdrant(context.Background(), "http://localhost", "", "c", 0)
	require.Error(t, err)
	_, err = vector.OpenQdrant(context.Background(), "://bad", "", "c", 1)
	require.Error(t, err)
}

func TestQdrant_EnsureCollectionErrors(t *testing.T) {
	t.Run("get unexpected status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`boom`))
		}))
		t.Cleanup(srv.Close)
		_, err := vector.OpenQdrant(context.Background(), srv.URL, "", "c", 2)
		require.Error(t, err)
	})
	t.Run("create failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`nope`))
		}))
		t.Cleanup(srv.Close)
		_, err := vector.OpenQdrant(context.Background(), srv.URL, "", "c", 2)
		require.Error(t, err)
	})
	t.Run("already exists via 400", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":{"error":"already exists"}}`))
		}))
		t.Cleanup(srv.Close)
		_, err := vector.OpenQdrant(context.Background(), srv.URL, "", "c", 2)
		require.NoError(t, err)
	})
}

func TestQdrant_PingErrorAndEmptyOps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	store, err := vector.OpenQdrant(context.Background(), srv.URL, "k", "c", 2)
	require.NoError(t, err)
	require.NoError(t, store.Upsert(context.Background(), nil))
	require.NoError(t, store.Delete(context.Background(), nil))

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(fail.Close)
	// Re-open against fail server for upsert error path after collection exists.
	store2, err := vector.OpenQdrant(context.Background(), fail.URL, "", "c", 2)
	require.NoError(t, err)
	err = store2.Upsert(context.Background(), []vector.Record{{ID: "x", Vector: []float32{1, 2}}})
	require.Error(t, err)
}

func TestOpenStore_Qdrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	store, err := vector.OpenStore(context.Background(), config.VectorStoreConfig{
		Provider:   "qdrant",
		URL:        srv.URL,
		Collection: "c",
	}, 8)
	require.NoError(t, err)
	require.NoError(t, store.Ping(context.Background()))
	require.NoError(t, store.Close())
}

func TestOpenStore_UnknownProvider(t *testing.T) {
	_, err := vector.OpenStore(context.Background(), config.VectorStoreConfig{Provider: "weaviate"}, 1)
	require.Error(t, err)
	_, err = vector.OpenStore(context.Background(), config.VectorStoreConfig{}, 1)
	require.Error(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = vector.OpenStore(ctx, config.VectorStoreConfig{
		Provider: "pgvector",
		DSN:      "postgres://u:p@127.0.0.1:1/db?connect_timeout=1",
		Table:    "t",
	}, 3)
	require.Error(t, err)
}

func TestQdrant_ErrorRedactsURLAndOmitsBody(t *testing.T) {
	const secret = "s3cret"
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`password=` + secret))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	u.User = url.UserPassword("user", secret)
	store, err := vector.OpenQdrant(context.Background(), u.String(), "", "c", 2)
	require.NoError(t, err)
	err = store.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), "password=")
}
