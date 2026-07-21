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
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

func TestVercel_TypeRegistered(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("vercel")
	require.NotNil(t, typ)
	assert.Equal(t, "vercel", typ.Name())
	assert.False(t, typ.PinsBatch())
	assert.Contains(t, typ.ComputedAuthHeaders(), "x-vercel-protection-bypass")
}

func TestVercel_ParamSpec(t *testing.T) {
	spec := action.VercelType{}.ParamSpec()
	assert.True(t, spec["url"].Required)
	assert.True(t, spec["url"].Secret)
	assert.False(t, spec["bypass-secret"].Required)
	assert.True(t, spec["bypass-secret"].Secret)
}

func TestVercel_Build_WithBypass(t *testing.T) {
	whCfg, _, err := action.VercelType{}.Build(action.ResolvedParams{
		"url":           "https://my-app.vercel.app/api/cdc",
		"bypass-secret": "bypass-tok",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://my-app.vercel.app/api/cdc", whCfg.URL)
	assert.Equal(t, "bypass-tok", whCfg.Headers["x-vercel-protection-bypass"])
}

func TestVercel_Build_NoBypass(t *testing.T) {
	whCfg, _, err := action.VercelType{}.Build(action.ResolvedParams{
		"url": "https://my-app.vercel.app/api/cdc",
	})
	require.NoError(t, err)
	assert.Empty(t, whCfg.Headers["x-vercel-protection-bypass"])
}

func TestVercel_GoldenRequest(t *testing.T) {
	var (
		gotBypass string
		gotBody   []byte
		gotMethod string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBypass = r.Header.Get("x-vercel-protection-bypass")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("VERCEL_FN_URL", srv.URL)
	t.Setenv("VERCEL_BYPASS", "protection-bypass-secret")
	reg := action.NewRegistry()
	reg.Register(action.VercelType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "vercel-hook",
			Type: "vercel",
			Params: map[string]string{
				"url":           "${VERCEL_FN_URL}",
				"bypass-secret": "${VERCEL_BYPASS}",
			},
		}},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := serverlessTestEntry(t, "idem-vercel-1")
	require.NoError(t, consumers[0].Deliver(context.Background(), entry))
	require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "protection-bypass-secret", gotBypass)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "orders", body["table"])
}

func TestVercel_SecretPolicy_LiteralURLRejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.VercelType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "bad-vercel",
			Type: "vercel",
			Params: map[string]string{
				"url": "https://my-app.vercel.app/api/cdc",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "url")
}

func TestVercel_SecretPolicy_LiteralBypassRejected(t *testing.T) {
	t.Setenv("VERCEL_FN_URL", "https://my-app.vercel.app/api/cdc")
	reg := action.NewRegistry()
	reg.Register(action.VercelType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "bad-vercel-bypass",
			Type: "vercel",
			Params: map[string]string{
				"url":           "${VERCEL_FN_URL}",
				"bypass-secret": "literal-bypass",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bypass-secret")
}

func TestVercel_BatchOverride_Allowed(t *testing.T) {
	t.Setenv("VERCEL_FN_URL", "https://my-app.vercel.app/api/cdc")
	reg := action.NewRegistry()
	reg.Register(action.VercelType{})
	batch := config.WebhookBatch{MaxEvents: 50}

	consumers, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "batched-vercel",
			Type: "vercel",
			Params: map[string]string{
				"url": "${VERCEL_FN_URL}",
			},
			Batch: &batch,
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}
