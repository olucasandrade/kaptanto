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
	"github.com/olucasandrade/kaptanto/internal/router"
)

func TestCloudflareWorker_TypeRegistered(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("cloudflare-worker")
	require.NotNil(t, typ)
	assert.Equal(t, "cloudflare-worker", typ.Name())
	assert.False(t, typ.PinsBatch())
}

func TestCloudflareWorker_ParamSpec(t *testing.T) {
	spec := action.CloudflareWorkerType{}.ParamSpec()
	assert.True(t, spec["url"].Required)
	assert.True(t, spec["url"].Secret)
	assert.Equal(t, "Authorization", spec["auth-header-name"].Default)
	assert.False(t, spec["auth-token"].Required)
	assert.True(t, spec["auth-token"].Secret)
	assert.False(t, spec["allow-unauthenticated"].Required)
	assert.False(t, spec["allow-unauthenticated"].Secret)
}

func TestCloudflareWorker_Build_WithAuth(t *testing.T) {
	whCfg, _, err := action.CloudflareWorkerType{}.Build(action.ResolvedParams{
		"url":              "https://worker.example.workers.dev/",
		"auth-header-name": "Authorization",
		"auth-token":       "Bearer secret-token",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://worker.example.workers.dev/", whCfg.URL)
	assert.Equal(t, "Bearer secret-token", whCfg.Headers["Authorization"])
	assert.Equal(t, 0, whCfg.Batch.MaxEvents)
}

func TestCloudflareWorker_Build_CustomAuthHeader(t *testing.T) {
	whCfg, _, err := action.CloudflareWorkerType{}.Build(action.ResolvedParams{
		"url":              "https://worker.example.workers.dev/",
		"auth-header-name": "X-Custom-Auth",
		"auth-token":       "tok-123",
	})
	require.NoError(t, err)
	assert.Equal(t, "tok-123", whCfg.Headers["X-Custom-Auth"])
	assert.Empty(t, whCfg.Headers["Authorization"])
}

func TestCloudflareWorker_Build_NoAuth_Rejected(t *testing.T) {
	_, _, err := action.CloudflareWorkerType{}.Build(action.ResolvedParams{
		"url": "https://worker.example.workers.dev/",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-token is empty")
	assert.Contains(t, err.Error(), "allow-unauthenticated")
}

func TestCloudflareWorker_Build_AllowUnauthenticatedOptIn(t *testing.T) {
	whCfg, _, err := action.CloudflareWorkerType{}.Build(action.ResolvedParams{
		"url":                     "https://worker.example.workers.dev/",
		"allow-unauthenticated":   "true",
	})
	require.NoError(t, err)
	assert.Empty(t, whCfg.Headers["Authorization"])
}

func TestCloudflareWorker_GoldenRequest(t *testing.T) {
	var (
		gotAuth   string
		gotBody   []byte
		gotMethod string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("CF_WORKER_URL", srv.URL)
	t.Setenv("CF_WORKER_TOKEN", "Bearer worker-secret")
	reg := action.NewRegistry()
	reg.Register(action.CloudflareWorkerType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "cf-worker",
			Type: "cloudflare-worker",
			Params: map[string]string{
				"url":        "${CF_WORKER_URL}",
				"auth-token": "${CF_WORKER_TOKEN}",
			},
		}},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := serverlessTestEntry(t, "idem-cfw-1")
	require.NoError(t, consumers[0].Deliver(context.Background(), entry))
	require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "Bearer worker-secret", gotAuth)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "orders", body["table"])
}

func TestCloudflareWorker_SecretPolicy_LiteralURLRejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.CloudflareWorkerType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "bad-cfw",
			Type: "cloudflare-worker",
			Params: map[string]string{
				"url": "https://worker.example.workers.dev/?token=secret",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "url")
}

func TestCloudflareWorker_SecretPolicy_LiteralAuthTokenRejected(t *testing.T) {
	t.Setenv("CF_WORKER_URL", "https://worker.example.workers.dev/")
	reg := action.NewRegistry()
	reg.Register(action.CloudflareWorkerType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "bad-cfw-token",
			Type: "cloudflare-worker",
			Params: map[string]string{
				"url":        "${CF_WORKER_URL}",
				"auth-token": "Bearer literal-secret",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-token")
}

func TestCloudflareWorker_BuildConsumers_NoAuthRequiresOptIn(t *testing.T) {
	t.Setenv("CF_WORKER_URL", "https://worker.example.workers.dev/")
	reg := action.NewRegistry()
	reg.Register(action.CloudflareWorkerType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "unauth-cfw",
			Type: "cloudflare-worker",
			Params: map[string]string{
				"url": "${CF_WORKER_URL}",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow-unauthenticated")
}

func TestCloudflareWorker_BatchOverride_Allowed(t *testing.T) {
	t.Setenv("CF_WORKER_URL", "https://worker.example.workers.dev/")
	reg := action.NewRegistry()
	reg.Register(action.CloudflareWorkerType{})
	batch := config.WebhookBatch{MaxEvents: 25}

	consumers, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "batched-cfw",
			Type: "cloudflare-worker",
			Params: map[string]string{
				"url":                   "${CF_WORKER_URL}",
				"allow-unauthenticated": "true",
			},
			Batch: &batch,
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}

func serverlessTestEntry(t *testing.T, idem string) eventlog.LogEntry {
	t.Helper()
	ev := &event.ChangeEvent{
		Schema:         "public",
		Table:          "orders",
		Operation:      event.OpInsert,
		Key:            json.RawMessage(`{"id":1}`),
		IdempotencyKey: idem,
		After:          json.RawMessage(`{"id":1}`),
	}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	return eventlog.LogEntry{Seq: 1, PartitionID: 0, Event: ev, Raw: raw}
}
