// Package sqssink provides SQSSinkConsumer, a router.Consumer implementation
// that publishes CDC events to an AWS SQS FIFO queue.
//
// Key design decisions:
//   - SQS is a stateless HTTP API — there is no persistent connection to close or
//     reconnect. Each SendMessage call is an independent HTTP request; the AWS SDK
//     handles retries and credential refresh internally.
//   - Deliver is synchronous (CHK-01): it blocks until SendMessage returns, so the
//     router's cursor does not advance until the broker has confirmed the write.
//   - MessageGroupId is derived from FNV-1a 64-bit hash of the primary key, giving
//     per-key FIFO ordering within the SQS FIFO queue (DLV-02 adaptation).
//   - MessageDeduplicationId is SHA-256[:64] of IdempotencyKey, satisfying SQS's
//     128-char limit while providing content-based deduplication (DLV-04).
//   - On SendMessage failure Deliver returns a non-nil error; retry is the
//     RetryScheduler's responsibility — SQSSinkConsumer never retries internally (DLV-03).
package sqssink

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertion: SQSSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*SQSSinkConsumer)(nil)

// sqsAPI is the interface subset of *sqs.Client used by SQSSinkConsumer.
// Extracting an interface enables test injection without a live AWS endpoint.
type sqsAPI interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// SQSSinkConsumer is a router.Consumer that publishes CDC events to an AWS SQS
// FIFO queue. It is safe for concurrent Deliver calls across different message
// group keys (RTR-04): the AWS SDK serialises concurrent HTTP requests internally.
//
// Use NewSQSSinkConsumer to construct — do not create directly.
type SQSSinkConsumer struct {
	id       string
	client   sqsAPI
	queueURL string
	m        *observability.KaptantoMetrics
}

// NewSQSSinkConsumer creates a SQSSinkConsumer that publishes to the FIFO queue
// identified by cfg.QueueURL.
//
// It returns a non-nil error when:
//   - GetQueueAttributes fails (access denied, queue not found, network error)
//   - The queue is not a FIFO queue (FifoQueue attribute != "true")
//
// Static credentials are used when both cfg.AccessKeyID and cfg.SecretAccessKey
// are non-empty; otherwise the full AWS credential chain applies
// (env vars → ~/.aws/credentials → IAM instance profile).
func NewSQSSinkConsumer(id string, cfg config.SQSSinkConfig) (*SQSSinkConsumer, error) {
	// 1. Build AWS SDK option functions.
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	// Wire custom CA when cfg.TLS.CAFile is set (CFG-03: SQS TLS CA pinning).
	if cfg.TLS.CAFile != "" {
		pemData, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("sqs sink: read ca-file %q: %w", cfg.TLS.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("sqs sink: ca-file %q: no valid PEM certificates found", cfg.TLS.CAFile)
		}
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    pool,
			},
		}
		opts = append(opts, awsconfig.WithHTTPClient(&http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}))
	}

	// 2. Load AWS config with a 10-second startup timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("sqs sink: load aws config: %w", err)
	}

	// 3. Create the SQS client (*sqs.Client satisfies sqsAPI).
	client := sqs.NewFromConfig(awsCfg)

	return newConsumerWithClient(id, cfg.QueueURL, client)
}

// newConsumerWithClient is the internal constructor used by both NewSQSSinkConsumer
// and tests. It validates that queueURL refers to a FIFO queue via GetQueueAttributes.
func newConsumerWithClient(id string, queueURL string, client sqsAPI) (*SQSSinkConsumer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs sink: get queue attributes for %q: %w", queueURL, err)
	}

	if out.Attributes[string(types.QueueAttributeNameFifoQueue)] != "true" {
		return nil, fmt.Errorf("sqs sink: %q is not a FIFO queue — SQS sink requires a .fifo queue URL", queueURL)
	}

	return &SQSSinkConsumer{
		id:       id,
		client:   client,
		queueURL: queueURL,
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
// It is the id argument passed to NewSQSSinkConsumer.
func (c *SQSSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *SQSSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver publishes entry.Event to the SQS FIFO queue synchronously (CHK-01).
//
// It blocks until SendMessage returns. The router's cursor is NOT advanced until
// this function returns nil, preserving at-least-once delivery semantics.
//
// MessageGroupId is set to FNV-1a 64-bit hex of entry.Event.Key (always 16 chars,
// within SQS's 128-char limit) to preserve per-key ordering in the FIFO queue.
//
// MessageDeduplicationId is set to SHA-256[:64] of entry.Event.IdempotencyKey
// (always 64 chars, within SQS's 128-char limit) for content-based deduplication.
//
// On error Deliver returns a non-nil error immediately. The RetryScheduler is
// responsible for rescheduling; Deliver never retries internally (DLV-03).
func (c *SQSSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. MessageGroupId: FNV-1a 64-bit hash of Key bytes, formatted as 16 zero-padded hex chars.
	h := fnv.New64a()
	h.Write(entry.Event.Key)
	groupID := fmt.Sprintf("%016x", h.Sum64())

	// 2. MessageDeduplicationId: SHA-256 hex of IdempotencyKey, truncated to 64 chars.
	sum := sha256.Sum256([]byte(entry.Event.IdempotencyKey))
	dedupID := fmt.Sprintf("%x", sum)[:64]

	// 3. Marshal the event to JSON for the message body.
	data, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("sqs sink: marshal event: %w", err)
	}

	// 4. Record send start time for latency observation.
	start := time.Now()

	// 5. Send the message to SQS.
	_, sendErr := c.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.queueURL),
		MessageBody:            aws.String(string(data)),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"Kaptanto-Idempotency-Key": {
				DataType:    aws.String("String"),
				StringValue: aws.String(entry.Event.IdempotencyKey),
			},
		},
	})

	// 6. Observe latency regardless of success/failure (only when metrics are wired).
	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("sqs").Observe(time.Since(start).Seconds())
	}

	if sendErr != nil {
		if c.m != nil {
			c.m.QueuePublishErrors.WithLabelValues("sqs").Inc()
		}
		return fmt.Errorf("sqs sink: send message to %q: %w", c.queueURL, sendErr)
	}

	if c.m != nil {
		c.m.QueuePublishTotal.WithLabelValues("sqs").Inc()
	}
	return nil
}

// Ping verifies the queue is reachable by calling GetQueueAttributes (read-only).
// Using GetQueueAttributes rather than SendMessage avoids producing side-effecting
// probe messages in the queue (OBS-02 groundwork).
func (c *SQSSinkConsumer) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(c.queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return fmt.Errorf("sqs sink: ping %q: %w", c.queueURL, err)
	}
	return nil
}

// Close is a no-op for SQSSinkConsumer. SQS is a stateless HTTP API — there is
// no persistent TCP connection or session to close. The AWS SDK manages HTTP
// connection pooling internally.
func (c *SQSSinkConsumer) Close() {
	// no-op: SQS uses stateless HTTP; AWS SDK manages connection pooling internally.
}
