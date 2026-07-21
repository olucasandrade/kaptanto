package action_test

import (
	"context"
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

const (
	testLambdaAccessKey = "AKIDEXAMPLE"
	testLambdaSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

func withLambdaAWSCreds(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", testLambdaAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", testLambdaSecretKey)
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
}

func TestLambda_TypeRegistered(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("lambda")
	require.NotNil(t, typ)
	assert.Equal(t, "lambda", typ.Name())
	assert.True(t, typ.PinsBatch())
	assert.Contains(t, typ.ComputedAuthHeaders(), "Authorization")
}

func TestLambda_ParamSpec(t *testing.T) {
	spec := action.LambdaType{}.ParamSpec()
	assert.True(t, spec["function-url"].Required)
	assert.True(t, spec["function-url"].Secret)
	assert.True(t, spec["region"].Required)
	assert.False(t, spec["region"].Secret)
	assert.False(t, spec["invocation"].Required)
	assert.Equal(t, "sync", spec["invocation"].Default)
}

func TestLambda_Build_Sync(t *testing.T) {
	whCfg, tc, err := action.LambdaType{}.Build(action.ResolvedParams{
		"function-url": "https://abc.lambda-url.us-east-1.on.aws/",
		"region":       "us-east-1",
		"invocation":   "sync",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://abc.lambda-url.us-east-1.on.aws/", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)
	require.NotNil(t, whCfg.Auth.AWSSigV4)
	assert.Equal(t, "us-east-1", whCfg.Auth.AWSSigV4.Region)
	assert.Equal(t, "lambda", whCfg.Auth.AWSSigV4.Service)
	assert.Empty(t, whCfg.Headers["X-Amz-Invocation-Type"])
	assert.Empty(t, tc.Language)
}

func TestLambda_Build_Async(t *testing.T) {
	whCfg, _, err := action.LambdaType{}.Build(action.ResolvedParams{
		"function-url": "https://abc.lambda-url.us-west-2.on.aws/",
		"region":       "us-west-2",
		"invocation":   "async",
	})
	require.NoError(t, err)
	assert.Equal(t, "Event", whCfg.Headers["X-Amz-Invocation-Type"])
}

func TestLambda_Build_InvalidInvocation(t *testing.T) {
	_, _, err := action.LambdaType{}.Build(action.ResolvedParams{
		"function-url": "https://example.com/",
		"region":       "us-east-1",
		"invocation":   "fire-and-forget",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invocation")
}

func TestLambda_GoldenRequest_AsyncSigV4_202Success(t *testing.T) {
	withLambdaAWSCreds(t)

	var (
		gotAuth    string
		gotInvType string
		gotBody    []byte
		gotMethod  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotInvType = r.Header.Get("X-Amz-Invocation-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted) // 202 — async Lambda success
	}))
	t.Cleanup(srv.Close)

	t.Setenv("LAMBDA_FUNCTION_URL", srv.URL)
	reg := action.NewRegistry()
	reg.Register(action.LambdaType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "invoke-orders",
			Type: "lambda",
			Params: map[string]string{
				"function-url": "${LAMBDA_FUNCTION_URL}",
				"region":       "us-east-1",
				"invocation":   "async",
			},
		}},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	entry := lambdaTestEntry(t)
	require.NoError(t, consumers[0].Deliver(context.Background(), entry))
	require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))

	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "Event", gotInvType)
	require.True(t, strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 "), "got Authorization=%q", gotAuth)
	assert.Contains(t, gotAuth, "Credential="+testLambdaAccessKey+"/")
	assert.Contains(t, gotAuth, "/us-east-1/lambda/aws4_request")
	assert.NotEmpty(t, gotBody)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &body))
	assert.Equal(t, "orders", body["table"])
}

func TestLambda_GoldenRequest_Sync(t *testing.T) {
	withLambdaAWSCreds(t)

	var gotInvType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInvType = r.Header.Get("X-Amz-Invocation-Type")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("LAMBDA_FUNCTION_URL", srv.URL)
	reg := action.NewRegistry()
	reg.Register(action.LambdaType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "invoke-sync",
			Type: "lambda",
			Params: map[string]string{
				"function-url": "${LAMBDA_FUNCTION_URL}",
				"region":       "eu-west-1",
			},
		}},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)

	entry := lambdaTestEntry(t)
	require.NoError(t, consumers[0].Deliver(context.Background(), entry))
	require.NoError(t, consumers[0].(router.BatchFlusher).FlushBatch(context.Background(), 0))
	assert.Empty(t, gotInvType)
}

func TestLambda_SecretPolicy_LiteralURLRejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.LambdaType{})

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "bad-lambda",
			Type: "lambda",
			Params: map[string]string{
				"function-url": "https://abc.lambda-url.us-east-1.on.aws/",
				"region":       "us-east-1",
			},
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "function-url")
}

func TestLambda_BatchOverride_Rejected(t *testing.T) {
	withLambdaAWSCreds(t)
	t.Setenv("LAMBDA_FUNCTION_URL", "https://example.com/")

	reg := action.NewRegistry()
	reg.Register(action.LambdaType{})
	batch := config.WebhookBatch{MaxEvents: 10}

	_, err := action.BuildConsumersWithRegistry(&config.Config{
		Actions: []config.ActionConfig{{
			Name: "batched-lambda",
			Type: "lambda",
			Params: map[string]string{
				"function-url": "${LAMBDA_FUNCTION_URL}",
				"region":       "us-east-1",
			},
			Batch: &batch,
		}},
	}, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func lambdaTestEntry(t *testing.T) eventlog.LogEntry {
	t.Helper()
	ev := &event.ChangeEvent{
		Schema:         "public",
		Table:          "orders",
		Operation:      event.OpInsert,
		Key:            json.RawMessage(`{"id":1}`),
		IdempotencyKey: "idem-lambda-1",
		After:          json.RawMessage(`{"id":1,"status":"new"}`),
	}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	return eventlog.LogEntry{Seq: 1, PartitionID: 0, Event: ev, Raw: raw}
}
