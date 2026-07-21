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

func TestCacheInvalidate_ParamSpec(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct, "cache-invalidate must be registered")

	spec := ct.ParamSpec()
	assert.True(t, spec["api-token"].Required)
	assert.True(t, spec["api-token"].Secret)
	assert.True(t, spec["zone-id"].Required)
	assert.False(t, spec["zone-id"].Secret)
	assert.True(t, spec["url-template"].Required)
	assert.False(t, spec["url-template"].Secret)
}

func TestCacheInvalidate_PinsBatch(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)
	assert.True(t, ct.PinsBatch())
}

func TestCacheInvalidate_ComputedAuthHeaders(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)
	assert.Equal(t, []string{"Authorization"}, ct.ComputedAuthHeaders())
}

func TestCacheInvalidate_Build_URL(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	whCfg, _, err := ct.Build(action.ResolvedParams{
		"api-token":    "cf-token-123",
		"zone-id":      "zone-abc",
		"url-template": `https://cdn.example.com/{{.Schema}}/{{.Table}}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.cloudflare.com/client/v4/zones/zone-abc/purge_cache", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, "cf-token-123", whCfg.Auth.BearerToken)
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)
}

func TestCacheInvalidate_Build_DefaultTransform(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	_, tc, err := ct.Build(action.ResolvedParams{
		"api-token":    "tok",
		"zone-id":      "z1",
		"url-template": `https://cdn.example.com/{{.Schema}}/{{.Table}}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "jq", tc.Language)
	assert.Contains(t, tc.Expression, `{"files":[`)
	assert.Contains(t, tc.Expression, ".schema")
	assert.Contains(t, tc.Expression, ".table")
}

func TestCacheInvalidate_MissingRequiredParams(t *testing.T) {
	t.Setenv("CF_TOKEN", "tok")
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("cache-invalidate"))
	m := observability.NewKaptantoMetrics()

	tests := []struct {
		name    string
		params  map[string]string
		missing string
	}{
		{
			name:    "missing api-token",
			params:  map[string]string{"zone-id": "z1", "url-template": "https://example.com"},
			missing: "api-token",
		},
		{
			name:    "missing zone-id",
			params:  map[string]string{"api-token": "${CF_TOKEN}", "url-template": "https://example.com"},
			missing: "zone-id",
		},
		{
			name:    "missing url-template",
			params:  map[string]string{"api-token": "${CF_TOKEN}", "zone-id": "z1"},
			missing: "url-template",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Actions: []config.ActionConfig{
					{Name: "purge", Type: "cache-invalidate", Params: tt.params},
				},
			}
			_, err := action.BuildConsumersWithRegistry(cfg, m, reg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.missing)
		})
	}
}

func TestCacheInvalidate_SecretRedacted(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("cache-invalidate"))
	m := observability.NewKaptantoMetrics()

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "purge",
				Type: "cache-invalidate",
				Params: map[string]string{
					"api-token":    "literal-token-not-env-ref",
					"zone-id":      "z1",
					"url-template": "https://example.com",
				},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, m, reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "api-token")
}

func TestCacheInvalidate_BatchOverrideRejected(t *testing.T) {
	t.Setenv("CF_TOKEN", "tok")
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("cache-invalidate"))
	m := observability.NewKaptantoMetrics()

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "purge",
				Type: "cache-invalidate",
				Params: map[string]string{
					"api-token":    "${CF_TOKEN}",
					"zone-id":      "z1",
					"url-template": "https://example.com",
				},
				Batch: &config.WebhookBatch{MaxEvents: 10},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, m, reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func TestCacheInvalidate_Build_QuotesAndBackslashes(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	_, tc, err := ct.Build(action.ResolvedParams{
		"api-token":    "tok",
		"zone-id":      "z1",
		"url-template": `https://cdn.example.com/path?x="quoted"&y=back\slash`,
	})
	require.NoError(t, err)

	// The literal portion must be JSON-escaped inside the jq string literal.
	assert.Contains(t, tc.Expression, `\"quoted\"`)
	assert.Contains(t, tc.Expression, `back\\slash`)
}

func TestCacheInvalidate_GoldenRequest(t *testing.T) {
	var (
		gotMethod string
		gotAuth   string
		gotCT     string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	whCfg, tc, err := ct.Build(action.ResolvedParams{
		"api-token":    "cf-test-token-abc123",
		"zone-id":      "zone-xyz789",
		"url-template": `https://cdn.example.com/{{.Schema}}/{{.Table}}`,
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-cf", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "products",
		Operation: event.OpUpdate,
		Key:       json.RawMessage(`{"id":42}`),
		After:     json.RawMessage(`{"id":42,"name":"Widget"}`),
	}
	raw, _ := json.Marshal(ev)
	entry := eventlog.LogEntry{
		PartitionID: 0,
		Event:       ev,
		Raw:         raw,
	}

	require.NoError(t, consumer.Deliver(context.Background(), entry))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "Bearer cf-test-token-abc123", gotAuth)
	assert.Equal(t, "application/json", gotCT)

	var body map[string][]string
	require.NoError(t, json.Unmarshal(gotBody, &body))
	require.Len(t, body["files"], 1)
	assert.Equal(t, "https://cdn.example.com/public/products", body["files"][0])
}

func TestCacheInvalidate_Build_AfterColumn(t *testing.T) {
	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	_, tc, err := ct.Build(action.ResolvedParams{
		"api-token":    "tok",
		"zone-id":      "z1",
		"url-template": `https://cdn.example.com/products/{{.After.id}}`,
	})
	require.NoError(t, err)
	assert.Contains(t, tc.Expression, `.after["id"]`)
	assert.Contains(t, tc.Expression, `| tostring`)
}

func TestCacheInvalidate_GoldenRequest_DatabaseSource(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ct := action.DefaultRegistry.Lookup("cache-invalidate")
	require.NotNil(t, ct)

	whCfg, tc, err := ct.Build(action.ResolvedParams{
		"api-token":    "tok",
		"zone-id":      "z1",
		"url-template": `https://cdn.example.com/api/v1/{{.Table}}`,
	})
	require.NoError(t, err)

	whCfg.URL = srv.URL
	whCfg.Transform = tc

	consumer, err := webhooksink.NewWebhookSinkConsumer("test-cf-db", whCfg)
	require.NoError(t, err)
	t.Cleanup(consumer.Close)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "pages",
		Operation: event.OpInsert,
		Key:       json.RawMessage(`{"id":7}`),
		After:     json.RawMessage(`{"id":7,"slug":"about-us"}`),
	}
	raw, _ := json.Marshal(ev)
	entry := eventlog.LogEntry{
		PartitionID: 0,
		Event:       ev,
		Raw:         raw,
	}

	require.NoError(t, consumer.Deliver(context.Background(), entry))
	require.NoError(t, consumer.FlushBatch(context.Background(), 0))

	var body map[string][]string
	require.NoError(t, json.Unmarshal(gotBody, &body))
	require.Len(t, body["files"], 1)
	assert.Equal(t, "https://cdn.example.com/api/v1/pages", body["files"][0])
}
