package webhooksink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

const (
	testSigV4AccessKey = "AKIDEXAMPLE"
	testSigV4SecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

func withStaticAWSCreds(t *testing.T) {
	t.Helper()
	prev := loadAWSDefaultConfig
	t.Cleanup(func() { loadAWSDefaultConfig = prev })
	loadAWSDefaultConfig = func(ctx context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     testSigV4AccessKey,
					SecretAccessKey: testSigV4SecretKey,
					Source:          "test",
				}, nil
			}),
		}, nil
	}
}

func withFailingAWSCreds(t *testing.T, cause error) {
	t.Helper()
	prev := loadAWSDefaultConfig
	t.Cleanup(func() { loadAWSDefaultConfig = prev })
	loadAWSDefaultConfig = func(ctx context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{}, cause
			}),
		}, nil
	}
}

func TestNewWebhookSinkConsumer_SigV4MutualExclusion(t *testing.T) {
	sigv4 := &config.WebhookSigV4{Region: "us-east-1"}

	t.Run("sigv4+bearer", func(t *testing.T) {
		_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:  "http://example.com",
			Auth: config.WebhookAuthConfig{BearerToken: "tok", AWSSigV4: sigv4},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "webhook sink:")
		assert.Contains(t, err.Error(), "auth.aws-sigv4 and auth.bearer-token are mutually exclusive")
	})

	t.Run("sigv4+basic", func(t *testing.T) {
		_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Auth: config.WebhookAuthConfig{
				Basic:    config.WebhookBasicAuth{Username: "u", Password: "p"},
				AWSSigV4: sigv4,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "auth.aws-sigv4 and auth.basic are mutually exclusive")
	})

	t.Run("sigv4+hmac", func(t *testing.T) {
		_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:     "http://example.com",
			Auth:    config.WebhookAuthConfig{AWSSigV4: sigv4},
			Signing: config.WebhookSigning{Secret: "whsec"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "auth.aws-sigv4 and signing.secret are mutually exclusive")
	})
}

func TestNewWebhookSinkConsumer_SigV4RegionRequired(t *testing.T) {
	_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL:  "http://example.com",
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.aws-sigv4.region is required")
}

func TestNewWebhookSinkConsumer_SigV4MissingCredentials(t *testing.T) {
	withFailingAWSCreds(t, fmt.Errorf("no AWS credentials found in the standard provider chain"))
	_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL:  "http://example.com",
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "us-west-2"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "webhook sink:")
	assert.Contains(t, err.Error(), "resolve aws credentials")
}

func TestNewWebhookSinkConsumer_SigV4ServiceDefaultsToLambda(t *testing.T) {
	withStaticAWSCreds(t)
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL:  "http://example.com",
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "eu-west-1"}},
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NotNil(t, c.sigv4)
	assert.Equal(t, "lambda", c.sigv4.service)
	assert.Equal(t, "eu-west-1", c.sigv4.region)
}

func TestNewWebhookSinkConsumer_SigV4CustomService(t *testing.T) {
	withStaticAWSCreds(t)
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL: "http://example.com",
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{
			Region:  "us-east-1",
			Service: "execute-api",
		}},
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	assert.Equal(t, "execute-api", c.sigv4.service)
}

func TestNewWebhookSinkConsumer_SigV4AuthorizationHeaderConflict(t *testing.T) {
	_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL:     "http://example.com",
		Headers: map[string]string{"Authorization": "Bearer x"},
		Auth:    config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "us-east-1"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authorization")
}

func TestFlushBatch_SigV4SignatureValid(t *testing.T) {
	withStaticAWSCreds(t)

	var (
		mu        sync.Mutex
		gotAuth   string
		gotDate   string
		gotHash   string
		gotBody   []byte
		gotHost   string
		gotMethod string
		reqURL    string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotBody = append([]byte(nil), body...)
		gotHost = r.Host
		gotMethod = r.Method
		reqURL = "http://" + r.Host + r.URL.RequestURI()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c, err := NewWebhookSinkConsumer("webhook", config.WebhookSinkConfig{
		URL:  srv.URL,
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "us-east-1"}},
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	raw := []byte(`{"id":1,"status":"ok"}`)
	require.NoError(t, c.Deliver(context.Background(), eventlog.LogEntry{
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: "insert",
			IdempotencyKey: "sigv4-1",
		},
		Raw: raw,
	}))
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, gotAuth)
	require.NotEmpty(t, gotDate)
	assert.True(t, strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 "))
	assert.Contains(t, gotAuth, "Credential="+testSigV4AccessKey+"/")
	assert.Contains(t, gotAuth, "/us-east-1/lambda/aws4_request")

	sum := sha256.Sum256(gotBody)
	wantHash := hex.EncodeToString(sum[:])
	assert.Equal(t, wantHash, gotHash)
	assert.Equal(t, raw, gotBody)

	signingTime, err := time.Parse("20060102T150405Z", gotDate)
	require.NoError(t, err)

	reReq, err := http.NewRequest(gotMethod, reqURL, bytes.NewReader(gotBody))
	require.NoError(t, err)
	reReq.Header.Set("Content-Type", "application/json")
	reReq.Header.Set("User-Agent", "kaptanto")
	reReq.Header.Set("X-Kaptanto-Idempotency-Key", "sigv4-1")
	reReq.Header.Set("X-Amz-Content-Sha256", wantHash)
	reReq.Host = gotHost

	signer := v4.NewSigner()
	creds := aws.Credentials{AccessKeyID: testSigV4AccessKey, SecretAccessKey: testSigV4SecretKey}
	require.NoError(t, signer.SignHTTP(context.Background(), creds, reReq, wantHash, "lambda", "us-east-1", signingTime))
	assert.Equal(t, reReq.Header.Get("Authorization"), gotAuth)
}

func TestFlushBatch_SigV4SignsAfterBatchJoin(t *testing.T) {
	withStaticAWSCreds(t)

	var (
		mu      sync.Mutex
		gotAuth string
		gotDate string
		gotHash string
		gotBody []byte
		gotHost string
		gotKeys string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotBody = append([]byte(nil), body...)
		gotHost = r.Host
		gotKeys = r.Header.Get("X-Kaptanto-Idempotency-Keys")
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c, err := NewWebhookSinkConsumer("webhook", config.WebhookSinkConfig{
		URL:   srv.URL,
		Batch: config.WebhookBatch{MaxEvents: 3},
		Auth:  config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "us-west-2", Service: "lambda"}},
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	bodies := [][]byte{[]byte(`{"n":0}`), []byte(`{"n":1}`), []byte(`{"n":2}`)}
	for i, b := range bodies {
		require.NoError(t, c.Deliver(context.Background(), eventlog.LogEntry{
			PartitionID: 0,
			Seq:         uint64(i),
			Event: &event.ChangeEvent{
				Schema: "public", Table: "orders", Operation: "insert",
				IdempotencyKey: fmt.Sprintf("b-%d", i),
			},
			Raw: b,
		}))
	}
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	mu.Lock()
	defer mu.Unlock()

	wantBody := []byte(`[{"n":0},{"n":1},{"n":2}]`)
	assert.Equal(t, wantBody, gotBody, "SigV4 must cover the joined JSON array body")
	assert.Equal(t, "b-0,b-1,b-2", gotKeys)

	sum := sha256.Sum256(wantBody)
	wantHash := hex.EncodeToString(sum[:])
	assert.Equal(t, wantHash, gotHash)

	signingTime, err := time.Parse("20060102T150405Z", gotDate)
	require.NoError(t, err)

	reReq, err := http.NewRequest(http.MethodPost, "http://"+gotHost+"/", bytes.NewReader(wantBody))
	require.NoError(t, err)
	reReq.Header.Set("Content-Type", "application/json")
	reReq.Header.Set("User-Agent", "kaptanto")
	reReq.Header.Set("X-Kaptanto-Idempotency-Keys", gotKeys)
	reReq.Header.Set("X-Amz-Content-Sha256", wantHash)
	reReq.Host = gotHost

	signer := v4.NewSigner()
	creds := aws.Credentials{AccessKeyID: testSigV4AccessKey, SecretAccessKey: testSigV4SecretKey}
	require.NoError(t, signer.SignHTTP(context.Background(), creds, reReq, wantHash, "lambda", "us-west-2", signingTime))
	assert.Equal(t, reReq.Header.Get("Authorization"), gotAuth)
}

func TestNewWebhookSinkConsumer_SigV4LoadConfigError(t *testing.T) {
	prev := loadAWSDefaultConfig
	t.Cleanup(func() { loadAWSDefaultConfig = prev })
	loadAWSDefaultConfig = func(ctx context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, fmt.Errorf("boom")
	}
	_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL:  "http://example.com",
		Auth: config.WebhookAuthConfig{AWSSigV4: &config.WebhookSigV4{Region: "us-east-1"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load aws config")
}
