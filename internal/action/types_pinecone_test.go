package action_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	webhooksink "github.com/olucasandrade/kaptanto/internal/output/webhook"
)

func TestVectorUpsert_ParamSpec(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt, "vector-upsert must be registered")

	spec := pt.ParamSpec()
	assert.True(t, spec["api-key"].Required)
	assert.True(t, spec["api-key"].Secret)
	assert.True(t, spec["index-host"].Required)
	assert.False(t, spec["index-host"].Secret)
	assert.False(t, spec["namespace"].Required)
	assert.False(t, spec["namespace"].Secret)
	assert.False(t, spec["id-field"].Required)
	assert.Equal(t, "id", spec["id-field"].Default)
	assert.True(t, spec["vector-field"].Required)
}

func TestVectorUpsert_PinsBatch(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)
	assert.False(t, pt.PinsBatch())
}

func TestVectorUpsert_ComputedAuthHeaders(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)
	assert.Equal(t, []string{"Api-Key"}, pt.ComputedAuthHeaders())
}

func TestVectorUpsert_Build_URL(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, _, err := pt.Build(action.ResolvedParams{
		"api-key":      "pc-key-123",
		"index-host":   "my-index-abc.svc.us-east1-gcp.pinecone.io",
		"vector-field": "embedding",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://my-index-abc.svc.us-east1-gcp.pinecone.io/vectors/upsert", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, "pc-key-123", whCfg.Headers["Api-Key"])
}

func TestVectorUpsert_Build_URLStripsHTTPS(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, _, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "https://my-index.svc.pinecone.io",
		"vector-field": "vec",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://my-index.svc.pinecone.io/vectors/upsert", whCfg.URL)
}

func TestVectorUpsert_Build_DefaultTransform(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	_, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"vector-field": "embedding",
	})
	require.NoError(t, err)
	assert.Equal(t, "jq", tc.Language)
	assert.Contains(t, tc.Expression, `"delete"`)
	assert.Contains(t, tc.Expression, "null")
	assert.Contains(t, tc.Expression, `.after["id"]`)
	assert.Contains(t, tc.Expression, `.after["embedding"]`)
	assert.Contains(t, tc.Expression, "tostring")
}

func TestVectorUpsert_Build_WithNamespace(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	_, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"namespace":    "prod-ns",
		"vector-field": "embedding",
	})
	require.NoError(t, err)
	assert.Contains(t, tc.Expression, `"prod-ns"`)
	assert.Contains(t, tc.Expression, "namespace")
}

func TestVectorUpsert_Build_CustomIDField(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	_, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"id-field":     "doc_id",
		"vector-field": "vec",
	})
	require.NoError(t, err)
	assert.Contains(t, tc.Expression, `.after["doc_id"]`)
}

func TestVectorUpsert_Build_HyphenField(t *testing.T) {
	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	_, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"vector-field": "embedding-vector",
	})
	require.NoError(t, err)
	assert.Contains(t, tc.Expression, `.after["embedding-vector"]`)
}

func TestVectorUpsert_MissingRequiredParams(t *testing.T) {
	t.Setenv("PC_KEY", "key")
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("vector-upsert"))
	m := observability.NewKaptantoMetrics()

	tests := []struct {
		name    string
		params  map[string]string
		missing string
	}{
		{
			name:    "missing api-key",
			params:  map[string]string{"index-host": "h", "vector-field": "v"},
			missing: "api-key",
		},
		{
			name:    "missing index-host",
			params:  map[string]string{"api-key": "${PC_KEY}", "vector-field": "v"},
			missing: "index-host",
		},
		{
			name:    "missing vector-field",
			params:  map[string]string{"api-key": "${PC_KEY}", "index-host": "h"},
			missing: "vector-field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Actions: []config.ActionConfig{
					{Name: "upsert", Type: "vector-upsert", Params: tt.params},
				},
			}
			_, err := action.BuildConsumersWithRegistry(cfg, m, reg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.missing)
		})
	}
}

func TestVectorUpsert_SecretRedacted(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("vector-upsert"))
	m := observability.NewKaptantoMetrics()

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "upsert",
				Type: "vector-upsert",
				Params: map[string]string{
					"api-key":      "literal-api-key",
					"index-host":   "host.pinecone.io",
					"vector-field": "embedding",
				},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, m, reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "api-key")
}

func TestVectorUpsert_GoldenRequest_Insert(t *testing.T) {
	var (
		gotMethod string
		gotApiKey string
		gotCT     string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotApiKey = r.Header.Get("Api-Key")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "pc-test-key-xyz",
		"index-host":   "my-index.svc.pinecone.io",
		"vector-field": "embedding",
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-pc", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "documents",
		Operation: event.OpInsert,
		Key:       json.RawMessage(`{"id":42}`),
		After:     json.RawMessage(`{"id":42,"title":"Hello","embedding":[0.1,0.2,0.3]}`),
	}
	raw, _ := json.Marshal(ev)

	require.NoError(t, consumer.Deliver(context.Background(), eventlog.LogEntry{
		PartitionID: 0, Event: ev, Raw: raw,
	}))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "pc-test-key-xyz", gotApiKey)
	assert.Equal(t, "application/json", gotCT)

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))

	vectors, ok := body["vectors"].([]any)
	require.True(t, ok, "expected vectors array")
	require.Len(t, vectors, 1)

	vec := vectors[0].(map[string]any)
	assert.Equal(t, "42", vec["id"], "id must be stringified")
	values := vec["values"].([]any)
	assert.InDelta(t, 0.1, values[0], 0.001)
	assert.InDelta(t, 0.2, values[1], 0.001)
	assert.InDelta(t, 0.3, values[2], 0.001)
}

func TestVectorUpsert_GoldenRequest_WithNamespace(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"namespace":    "production",
		"vector-field": "vec",
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-pc-ns", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "items",
		Operation: event.OpInsert,
		Key:       json.RawMessage(`{"id":7}`),
		After:     json.RawMessage(`{"id":7,"vec":[1.0,2.0]}`),
	}
	raw, _ := json.Marshal(ev)

	require.NoError(t, consumer.Deliver(context.Background(), eventlog.LogEntry{
		PartitionID: 0, Event: ev, Raw: raw,
	}))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "production", body["namespace"])
}

func TestVectorUpsert_GoldenRequest_StringID(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"id-field":     "doc_id",
		"vector-field": "embedding",
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-pc-strid", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "docs",
		Operation: event.OpUpdate,
		Key:       json.RawMessage(`{"doc_id":"abc-123"}`),
		After:     json.RawMessage(`{"doc_id":"abc-123","embedding":[0.5,0.6]}`),
	}
	raw, _ := json.Marshal(ev)

	require.NoError(t, consumer.Deliver(context.Background(), eventlog.LogEntry{
		PartitionID: 0, Event: ev, Raw: raw,
	}))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	var body map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &body))

	vectors := body["vectors"].([]any)
	vec := vectors[0].(map[string]any)
	assert.Equal(t, "abc-123", vec["id"], "string IDs must remain strings after tostring")
}

func TestVectorUpsert_DeleteEvent_Dropped(t *testing.T) {
	requestReceived := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	pt := action.DefaultRegistry.Lookup("vector-upsert")
	require.NotNil(t, pt)

	whCfg, tc, err := pt.Build(action.ResolvedParams{
		"api-key":      "key",
		"index-host":   "host.pinecone.io",
		"vector-field": "embedding",
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-pc-del", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "documents",
		Operation: event.OpDelete,
		Key:       json.RawMessage(`{"id":99}`),
		Before:    json.RawMessage(`{"id":99,"embedding":[0.1,0.2]}`),
		After:     nil,
	}
	raw, _ := json.Marshal(ev)

	require.NoError(t, consumer.Deliver(context.Background(), eventlog.LogEntry{
		PartitionID: 0, Event: ev, Raw: raw,
	}))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	assert.False(t, requestReceived, "delete events must be dropped (no HTTP request)")
}
