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

func TestHTTPRequest_TypeRegistered(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("http-request")
	require.NotNil(t, typ)
	assert.Equal(t, "http-request", typ.Name())
}

func TestHTTPRequest_ParamSpec(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("http-request")
	spec := typ.ParamSpec()
	assert.True(t, spec["url"].Required)
	assert.True(t, spec["url"].Secret)
	assert.False(t, spec["method"].Required)
	assert.Equal(t, "POST", spec["method"].Default)
}

func TestHTTPRequest_GoldenRequest_RawEventBody(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("HTTP_URL", srv.URL)
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("http-request"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "post-events",
				Type:   "http-request",
				Params: map[string]string{"url": "${HTTP_URL}"},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	rawEvent := json.RawMessage(`{"id":1,"status":"created"}`)
	entry := eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "idem-001",
			After:          rawEvent,
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "idem-001",
			After:          rawEvent,
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "application/json", gotHeaders.Get("Content-Type"))
	assert.NotEmpty(t, gotHeaders.Get("X-Kaptanto-Idempotency-Key"))

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "public", body["schema"])
	assert.Equal(t, "orders", body["table"])
}

func TestHTTPRequest_GoldenRequest_MethodOverride(t *testing.T) {
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("HTTP_URL", srv.URL)
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("http-request"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "put-events",
				Type:   "http-request",
				Params: map[string]string{"url": "${HTTP_URL}", "method": "PUT"},
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
			IdempotencyKey: "idem-002",
			After:          json.RawMessage(`{"id":1}`),
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "idem-002",
			After:          json.RawMessage(`{"id":1}`),
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.Equal(t, "PUT", gotMethod)
}

func TestHTTPRequest_SecretPolicy_LiteralURLRejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("http-request"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "bad-literal",
				Type:   "http-request",
				Params: map[string]string{"url": "https://api.example.com/webhook?token=secret123"},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "url")
}

func TestHTTPRequest_StandardHeaders(t *testing.T) {
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("HTTP_URL", srv.URL)
	reg := action.NewRegistry()
	reg.Register(action.DefaultRegistry.Lookup("http-request"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "headers-test",
				Type:   "http-request",
				Params: map[string]string{"url": "${HTTP_URL}"},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)

	entry := eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "idem-003",
			After:          json.RawMessage(`{"id":1}`),
		},
		Raw: mustMarshalEvent(t, &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      event.OpInsert,
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "idem-003",
			After:          json.RawMessage(`{"id":1}`),
		}),
	}

	err = consumers[0].Deliver(context.Background(), entry)
	require.NoError(t, err)

	bf := consumers[0].(router.BatchFlusher)
	err = bf.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	assert.Equal(t, "application/json", gotHeaders.Get("Content-Type"))
	assert.NotEmpty(t, gotHeaders.Get("X-Kaptanto-Idempotency-Key"))
}

func TestHTTPRequest_PinsBatch_False(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("http-request")
	assert.False(t, typ.PinsBatch())
}

func TestHTTPRequest_NoComputedAuthHeaders(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("http-request")
	assert.Nil(t, typ.ComputedAuthHeaders())
}

func mustMarshalEvent(t *testing.T, e *event.ChangeEvent) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	return b
}
