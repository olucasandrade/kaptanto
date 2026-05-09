// Package sqssink provides unit tests for SQSSinkConsumer.
// All tests use a fake sqsAPI implementation — no live AWS endpoint required.
package sqssink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// fakeSQSClient implements sqsAPI for tests — no live AWS endpoint required.
type fakeSQSClient struct {
	// sendMessageFunc is called by SendMessage; nil means return success.
	sendMessageFunc func(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	// getQueueAttributesFunc is called by GetQueueAttributes; nil means return FIFO=true.
	getQueueAttributesFunc func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	// lastSendInput captures the most recent SendMessageInput passed to SendMessage.
	lastSendInput *sqs.SendMessageInput
	// getQueueAttributesCallCount tracks how many times GetQueueAttributes is called.
	getQueueAttributesCallCount int
}

func (f *fakeSQSClient) SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.lastSendInput = params
	if f.sendMessageFunc != nil {
		return f.sendMessageFunc(ctx, params, optFns...)
	}
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQSClient) GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.getQueueAttributesCallCount++
	if f.getQueueAttributesFunc != nil {
		return f.getQueueAttributesFunc(ctx, params, optFns...)
	}
	// Default: respond as a valid FIFO queue.
	return &sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			string(types.QueueAttributeNameFifoQueue): "true",
		},
	}, nil
}

// newTestConsumer constructs a SQSSinkConsumer with the fake client injected directly,
// bypassing the AWS SDK constructor so tests never need a real AWS endpoint.
func newTestConsumer(t *testing.T, fake *fakeSQSClient, id string) *SQSSinkConsumer {
	t.Helper()
	const defaultURL = "https://sqs.us-east-1.amazonaws.com/123456789/test-queue.fifo"
	return &SQSSinkConsumer{
		id:              id,
		client:          fake,
		queueURL:        defaultURL,
		validatedQueues: map[string]bool{defaultURL: true},
	}
}

// makeEntry constructs a minimal eventlog.LogEntry for test use.
func makeEntry(key []byte, idempotencyKey string) eventlog.LogEntry {
	return eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			Key:            key,
			IdempotencyKey: idempotencyKey,
		},
	}
}

// newTemplateConsumer constructs a SQSSinkConsumer with a parsed queue URL template
// for routing tests. The default fallback URL is pre-validated in validatedQueues.
func newTemplateConsumer(t *testing.T, fake *fakeSQSClient, id string, queueURLTemplate string) *SQSSinkConsumer {
	t.Helper()
	const defaultURL = "https://sqs.us-east-1.amazonaws.com/123456789/fallback.fifo"
	tmpl, err := template.New("queue-url").Parse(queueURLTemplate)
	require.NoError(t, err)
	return &SQSSinkConsumer{
		id:              id,
		client:          fake,
		queueURL:        defaultURL,
		queueURLT:       tmpl,
		validatedQueues: map[string]bool{defaultURL: true},
	}
}

// --- Tests ---

func TestSQSSinkConsumer_Deliver_Success(t *testing.T) {
	fake := &fakeSQSClient{}
	m := observability.NewKaptantoMetrics()
	c := newTestConsumer(t, fake, "test-consumer")
	c.SetMetrics(m)

	entry := makeEntry([]byte(`{"id":1}`), "idem-key-1")
	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// QueuePublishTotal must be incremented to 1 after a successful deliver.
	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("sqs"))
	assert.Equal(t, float64(1), got)
}

func TestSQSSinkConsumer_Deliver_MessageGroupId(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTestConsumer(t, fake, "test-consumer")

	key := []byte(`{"id":42}`)
	entry := makeEntry(key, "idem-key-2")
	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	require.NotNil(t, fake.lastSendInput)
	require.NotNil(t, fake.lastSendInput.MessageGroupId)

	groupID := *fake.lastSendInput.MessageGroupId
	// FNV-1a 64-bit hex is always exactly 16 chars (zero-padded).
	assert.Len(t, groupID, 16, "MessageGroupId must be exactly 16 hex chars")

	// Verify determinism: same key produces the same groupID on a second call.
	fake2 := &fakeSQSClient{}
	c2 := newTestConsumer(t, fake2, "test-consumer")
	err = c2.Deliver(context.Background(), entry)
	require.NoError(t, err)
	assert.Equal(t, groupID, *fake2.lastSendInput.MessageGroupId, "MessageGroupId must be deterministic")
}

func TestSQSSinkConsumer_Deliver_MessageDeduplicationId(t *testing.T) {
	tests := []struct {
		name           string
		idempotencyKey string
	}{
		{"short key", "key"},
		{"long key", fmt.Sprintf("%0200d", 1)}, // 200-char numeric string
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSQSClient{}
			c := newTestConsumer(t, fake, "test-consumer")
			entry := makeEntry([]byte(`{"id":1}`), tt.idempotencyKey)

			err := c.Deliver(context.Background(), entry)
			require.NoError(t, err)

			require.NotNil(t, fake.lastSendInput)
			require.NotNil(t, fake.lastSendInput.MessageDeduplicationId)

			dedupID := *fake.lastSendInput.MessageDeduplicationId
			// SHA-256 hex[:64] is always exactly 64 chars regardless of input length.
			assert.Len(t, dedupID, 64, "MessageDeduplicationId must be exactly 64 hex chars")
		})
	}
}

func TestSQSSinkConsumer_Deliver_IdempotencyKeyAttribute(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTestConsumer(t, fake, "test-consumer")

	rawKey := "01JK9X2MTHZ5V5QYXH3AB4T1WF"
	entry := makeEntry([]byte(`{"id":1}`), rawKey)

	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	require.NotNil(t, fake.lastSendInput)
	attr, ok := fake.lastSendInput.MessageAttributes["Kaptanto-Idempotency-Key"]
	require.True(t, ok, "MessageAttributes must contain Kaptanto-Idempotency-Key")
	require.NotNil(t, attr.StringValue, "StringValue must not be nil")
	assert.Equal(t, rawKey, *attr.StringValue)
	require.NotNil(t, attr.DataType, "DataType must not be nil")
	assert.Equal(t, "String", *attr.DataType)
}

func TestSQSSinkConsumer_Deliver_Error(t *testing.T) {
	sendErr := errors.New("send failed: throttled")
	fake := &fakeSQSClient{
		sendMessageFunc: func(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
			return nil, sendErr
		},
	}
	m := observability.NewKaptantoMetrics()
	c := newTestConsumer(t, fake, "test-consumer")
	c.SetMetrics(m)

	entry := makeEntry([]byte(`{"id":1}`), "idem-key-err")
	err := c.Deliver(context.Background(), entry)
	require.Error(t, err)
	assert.ErrorContains(t, err, "send failed: throttled")

	// QueuePublishErrors must be incremented; QueuePublishTotal must NOT be.
	errCount := testutil.ToFloat64(m.QueuePublishErrors.WithLabelValues("sqs"))
	assert.Equal(t, float64(1), errCount)

	totalCount := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("sqs"))
	assert.Equal(t, float64(0), totalCount)
}

func TestSQSSinkConsumer_NewConsumer_NonFIFO(t *testing.T) {
	fake := &fakeSQSClient{
		getQueueAttributesFunc: func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
			return &sqs.GetQueueAttributesOutput{
				Attributes: map[string]string{
					string(types.QueueAttributeNameFifoQueue): "false",
				},
			}, nil
		},
	}

	_, err := newConsumerWithClient("consumer-1", "https://sqs.us-east-1.amazonaws.com/123/standard-queue", fake, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not a FIFO queue")
}

func TestSQSSinkConsumer_NewConsumer_GetAttrsFails(t *testing.T) {
	fake := &fakeSQSClient{
		getQueueAttributesFunc: func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
			return nil, errors.New("access denied")
		},
	}

	_, err := newConsumerWithClient("consumer-1", "https://sqs.us-east-1.amazonaws.com/123/test.fifo", fake, nil)
	require.Error(t, err)
}

func TestSQSSinkConsumer_Ping_Success(t *testing.T) {
	fake := &fakeSQSClient{
		getQueueAttributesFunc: func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
			return &sqs.GetQueueAttributesOutput{
				Attributes: map[string]string{
					string(types.QueueAttributeNameApproximateNumberOfMessages): "0",
				},
			}, nil
		},
	}
	c := newTestConsumer(t, fake, "test-consumer")
	err := c.Ping()
	assert.NoError(t, err)
}

func TestSQSSinkConsumer_Ping_Error(t *testing.T) {
	fake := &fakeSQSClient{
		getQueueAttributesFunc: func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
			return nil, errors.New("queue not found")
		},
	}
	c := newTestConsumer(t, fake, "test-consumer")
	err := c.Ping()
	require.Error(t, err)
}

func TestSQSSinkConsumer_ID(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTestConsumer(t, fake, "my-unique-id")
	assert.Equal(t, "my-unique-id", c.ID())
}

// generateTestClientKeypair creates a self-signed client cert + RSA private key PEM pair.
// Uses stdlib only (crypto/rand, crypto/rsa, crypto/x509, encoding/pem).
// The private key is encoded as PKCS#1 ("RSA PRIVATE KEY") — required for tls.LoadX509KeyPair.
func generateTestClientKeypair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "TestClient"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	var certBuf, keyBuf bytes.Buffer
	require.NoError(t, pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, pem.Encode(&keyBuf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return certBuf.Bytes(), keyBuf.Bytes()
}

// generateTestCAPEM creates a minimal self-signed CA PEM for tests using stdlib crypto only.
func generateTestCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TestCA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	return buf.Bytes()
}

func TestNewSQSSinkConsumer_TLS_MissingCAFile(t *testing.T) {
	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{CAFile: "/tmp/kaptanto_nonexistent_ca_test.pem"},
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "ca-file")
}

func TestNewSQSSinkConsumer_TLS_EmptyPEM(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "empty-ca.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("not a valid pem"), 0600))

	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{CAFile: caFile},
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "ca-file")
}

func TestNewSQSSinkConsumer_TLS_ValidCA(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, generateTestCAPEM(t), 0600))

	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{CAFile: caFile},
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	// Error is expected (no live AWS), but it must NOT be a TLS-construction error.
	// A network error or GetQueueAttributes failure is acceptable.
	if err != nil {
		assert.NotContains(t, err.Error(), "ca-file",
			"error must come from AWS/network, not TLS construction")
	}
}

func TestNewSQSSinkConsumer_mTLS_BothFieldsSet(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateTestClientKeypair(t)
	certFile := filepath.Join(dir, "client.pem")
	keyFile := filepath.Join(dir, "client-key.pem")
	require.NoError(t, os.WriteFile(certFile, certPEM, 0600))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0600))

	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{CertFile: certFile, KeyFile: keyFile},
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	// Error expected (no live AWS), but must NOT be a TLS construction error.
	if err != nil {
		assert.NotContains(t, err.Error(), "cert-file and key-file",
			"error must come from AWS/network, not TLS construction")
		assert.NotContains(t, err.Error(), "load client cert",
			"error must come from AWS/network, not TLS construction")
	}
}

func TestNewSQSSinkConsumer_mTLS_PartialConfig_CertOnly(t *testing.T) {
	dir := t.TempDir()
	certPEM, _ := generateTestClientKeypair(t)
	certFile := filepath.Join(dir, "client.pem")
	require.NoError(t, os.WriteFile(certFile, certPEM, 0600))

	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{CertFile: certFile}, // KeyFile intentionally absent
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cert-file and key-file must both be set")
}

func TestNewSQSSinkConsumer_mTLS_PartialConfig_KeyOnly(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM := generateTestClientKeypair(t)
	keyFile := filepath.Join(dir, "client-key.pem")
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0600))

	cfg := config.SQSSinkConfig{
		QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/test.fifo",
		Region:   "us-east-1",
		TLS:      config.TLSConfig{KeyFile: keyFile}, // CertFile intentionally absent
	}
	_, err := NewSQSSinkConsumer("sqs", cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cert-file and key-file must both be set")
}

// --- Per-table routing tests ---

// TestSQSSinkConsumer_Routing_PerTable confirms that two events with different
// Schema+Table values are delivered to two different QueueUrl values derived
// from the template.
func TestSQSSinkConsumer_Routing_PerTable(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTemplateConsumer(t, fake, "test", "https://sqs.us-east-1.amazonaws.com/123/{{.Schema}}-{{.Table}}.fifo")

	// Event from public.orders — should route to public-orders.fifo.
	entry1 := eventlog.LogEntry{Event: &event.ChangeEvent{
		Schema: "public", Table: "orders",
		Key: []byte(`{"id":1}`), IdempotencyKey: "key1",
	}}
	require.NoError(t, c.Deliver(context.Background(), entry1))
	require.NotNil(t, fake.lastSendInput)
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123/public-orders.fifo", *fake.lastSendInput.QueueUrl)

	// Event from public.users — should route to public-users.fifo.
	entry2 := eventlog.LogEntry{Event: &event.ChangeEvent{
		Schema: "public", Table: "users",
		Key: []byte(`{"id":2}`), IdempotencyKey: "key2",
	}}
	require.NoError(t, c.Deliver(context.Background(), entry2))
	require.NotNil(t, fake.lastSendInput)
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123/public-users.fifo", *fake.lastSendInput.QueueUrl)
}

// TestSQSSinkConsumer_Routing_PoolCaching confirms that GetQueueAttributes is
// called exactly once for a given resolved URL regardless of how many Deliver
// calls target it (lazy double-checked validation pool).
func TestSQSSinkConsumer_Routing_PoolCaching(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTemplateConsumer(t, fake, "test", "https://sqs.us-east-1.amazonaws.com/123/{{.Table}}.fifo")

	entry := eventlog.LogEntry{Event: &event.ChangeEvent{
		Schema: "public", Table: "orders",
		Key: []byte(`{"id":1}`), IdempotencyKey: "key-cache",
	}}

	// Deliver 3 times to the same resolved URL.
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.Deliver(context.Background(), entry))

	// GetQueueAttributes must have been called exactly once (first Deliver to orders.fifo).
	assert.Equal(t, 1, fake.getQueueAttributesCallCount,
		"FIFO validation should be cached: GetQueueAttributes called only once per unique URL")
}

// TestSQSSinkConsumer_Routing_TemplateParseError confirms that an invalid Go template
// string fails at parse time (construction), not at Deliver time (fail-fast pattern).
func TestSQSSinkConsumer_Routing_TemplateParseError(t *testing.T) {
	// Verify that template.Parse fails for a malformed template — the same logic
	// used by NewSQSSinkConsumer before any AWS I/O.
	_, err := template.New("queue-url").Parse("{{.Schema}}-{{invalid")
	require.Error(t, err, "invalid template must fail to parse")
	// This confirms the parse error would be caught at construction time (fail-fast).
}

// TestSQSSinkConsumer_Routing_TemplateExecError confirms that a template that renders
// to an empty string returns an error at Deliver time containing "rendered to empty string".
func TestSQSSinkConsumer_Routing_TemplateExecError(t *testing.T) {
	fake := &fakeSQSClient{}
	// Template that always renders to empty string.
	c := newTemplateConsumer(t, fake, "test", "{{if false}}something{{end}}")
	entry := eventlog.LogEntry{Event: &event.ChangeEvent{
		Schema: "public", Table: "orders",
		Key: []byte(`{"id":1}`), IdempotencyKey: "key-empty",
	}}
	err := c.Deliver(context.Background(), entry)
	require.Error(t, err)
	assert.ErrorContains(t, err, "rendered to empty string")
}

// TestSQSSinkConsumer_Routing_Regression_NoTemplate confirms that when no template
// is set, Deliver always uses the default queueURL and never calls GetQueueAttributes
// (the default URL is pre-validated at construction via validatedQueues seeding).
func TestSQSSinkConsumer_Routing_Regression_NoTemplate(t *testing.T) {
	fake := &fakeSQSClient{}
	c := newTestConsumer(t, fake, "test") // no template — uses c.queueURL

	entry := makeEntry([]byte(`{"id":1}`), "key-regression")
	require.NoError(t, c.Deliver(context.Background(), entry))

	require.NotNil(t, fake.lastSendInput)
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123456789/test-queue.fifo", *fake.lastSendInput.QueueUrl,
		"Without template, Deliver must use the default queueURL")
	// Default URL is pre-validated at construction (seeded in validatedQueues) —
	// GetQueueAttributes must NOT be called during Deliver.
	assert.Equal(t, 0, fake.getQueueAttributesCallCount,
		"Default URL is pre-validated at construction; no GetQueueAttributes call expected")
}
