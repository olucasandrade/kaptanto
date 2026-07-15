package action_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCustom_TypeRegistered(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("custom")
	require.NotNil(t, typ)
	assert.Equal(t, "custom", typ.Name())
}

func TestCustom_NoParams(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("custom")
	assert.Empty(t, typ.ParamSpec())
}

func TestCustom_MissingWebhookBlock_StartsError(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "bare-custom",
				Type: "custom",
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a webhook: block")
}

func TestCustom_VerbatimWebhookConfig(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "full-custom",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:    srv.URL,
					Method: "PUT",
					Headers: map[string]string{
						"X-Custom-Header": "custom-value",
					},
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":42}`),
			IdempotencyKey: "custom-idem-001",
			After:          json.RawMessage(`{"id":42,"total":100}`),
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":42}`),
			IdempotencyKey: "custom-idem-001",
			After:          json.RawMessage(`{"id":42,"total":100}`),
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.Equal(t, "custom-value", gotHeaders.Get("X-Custom-Header"))
	assert.NotEmpty(t, gotBody)
}

func TestCustom_Batching_Honored(t *testing.T) {
	var requestCount int
	var lastBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "batch-custom",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:    srv.URL,
					Method: "POST",
					Batch:  config.WebhookBatch{MaxEvents: 3},
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	for i := 0; i < 3; i++ {
		entry := eventlog.LogEntry{
			Seq:         uint64(i + 1),
			PartitionID: 0,
			Event: &event.ChangeEvent{
				Schema:         "public",
				Table:          "orders",
				Operation:      event.OpInsert,
				Key:            json.RawMessage(`{"id":` + strings.Repeat("1", i+1) + `}`),
				IdempotencyKey: "batch-idem-" + strings.Repeat("x", i+1),
				After:          json.RawMessage(`{"id":` + strings.Repeat("1", i+1) + `}`),
			},
			Raw: mustMarshalEvent(t, &event.ChangeEvent{
				Schema:         "public",
				Table:          "orders",
				Operation:      event.OpInsert,
				Key:            json.RawMessage(`{"id":` + strings.Repeat("1", i+1) + `}`),
				IdempotencyKey: "batch-idem-" + strings.Repeat("x", i+1),
				After:          json.RawMessage(`{"id":` + strings.Repeat("1", i+1) + `}`),
			}),
		}
		err = consumers[0].Deliver(context.Background(), entry)
		require.NoError(t, err)
	}

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.Equal(t, 1, requestCount, "batching should send 3 events in 1 request")

	var arr []json.RawMessage
	require.NoError(t, json.Unmarshal(lastBody, &arr))
	assert.Len(t, arr, 3)
}

func TestCustom_Signing_Honored(t *testing.T) {
	t.Setenv("CUSTOM_SIGNING_SECRET", "my-hmac-secret")
	secret := "my-hmac-secret"
	var gotBody []byte
	var gotSignature string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get("X-Kaptanto-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "signed-custom",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:     srv.URL,
					Method:  "POST",
					Signing: config.WebhookSigning{Secret: "${CUSTOM_SIGNING_SECRET}"},
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "sign-idem-001",
			After:          json.RawMessage(`{"id":1,"status":"new"}`),
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "sign-idem-001",
			After:          json.RawMessage(`{"id":1,"status":"new"}`),
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.NotEmpty(t, gotSignature, "signature header must be present")

	// Format: "t=<unix>,v1=<hex>" — Stripe-style (WHK-03)
	assert.True(t, strings.HasPrefix(gotSignature, "t="))
	assert.Contains(t, gotSignature, ",v1=")

	// Extract timestamp and hex signature
	commaIdx := strings.Index(gotSignature, ",v1=")
	require.Greater(t, commaIdx, 2)
	timestamp := gotSignature[2:commaIdx]
	sigHex := gotSignature[commaIdx+4:]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(gotBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	assert.Equal(t, expected, sigHex)
}

func TestCustom_Transform_Honored(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "transform-custom",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:    srv.URL,
					Method: "POST",
					Transform: config.TransformConfig{
						Language:   "jq",
						Expression: `{table: .table, op: .operation}`,
					},
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "tf-idem-001",
			After:          json.RawMessage(`{"id":1}`),
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "tf-idem-001",
			After:          json.RawMessage(`{"id":1}`),
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "orders", body["table"])
	assert.Equal(t, "insert", body["op"])
}

func TestCustom_LiteralSecret_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "literal-secret",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:     srv.URL,
					Signing: config.WebhookSigning{Secret: "hard-coded-secret"},
				},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing.secret")
	assert.Contains(t, err.Error(), "environment variable reference")
}

func TestCustom_PermissiveExpansion_ExpandToEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("custom"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "expand-empty",
				Type: "custom",
				Webhook: &config.WebhookSinkConfig{
					URL:    srv.URL,
					Method: "POST",
					Headers: map[string]string{
						"X-Token": "${UNSET_VAR_SHOULD_EXPAND_EMPTY}",
					},
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}

func TestCustom_PinsBatch_False(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("custom")
	assert.False(t, typ.PinsBatch())
}

func TestCustom_NoComputedAuthHeaders(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("custom")
	assert.Nil(t, typ.ComputedAuthHeaders())
}
