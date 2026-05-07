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
}

func (f *fakeSQSClient) SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.lastSendInput = params
	if f.sendMessageFunc != nil {
		return f.sendMessageFunc(ctx, params, optFns...)
	}
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQSClient) GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
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
	return &SQSSinkConsumer{
		id:       id,
		client:   fake,
		queueURL: "https://sqs.us-east-1.amazonaws.com/123456789/test-queue.fifo",
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

	_, err := newConsumerWithClient("consumer-1", "https://sqs.us-east-1.amazonaws.com/123/standard-queue", fake)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not a FIFO queue")
}

func TestSQSSinkConsumer_NewConsumer_GetAttrsFails(t *testing.T) {
	fake := &fakeSQSClient{
		getQueueAttributesFunc: func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
			return nil, errors.New("access denied")
		},
	}

	_, err := newConsumerWithClient("consumer-1", "https://sqs.us-east-1.amazonaws.com/123/test.fifo", fake)
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
