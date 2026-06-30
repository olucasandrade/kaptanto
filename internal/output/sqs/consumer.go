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
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
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
	SendMessageBatch(ctx context.Context, params *sqs.SendMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// pendingSQSMessage holds the pre-encoded fields for a single buffered message,
// ready to be included in a SendMessageBatch entry.
type pendingSQSMessage struct {
	queueURL string
	entry    types.SendMessageBatchRequestEntry
}

// SQSSinkConsumer is a router.Consumer that publishes CDC events to an AWS SQS
// FIFO queue. It is safe for concurrent Deliver calls across different message
// group keys (RTR-04): the AWS SDK serialises concurrent HTTP requests internally.
//
// When used with the Router's BatchFlusher interface, Deliver enqueues messages
// into a per-consumer buffer and FlushBatch sends them via SendMessageBatch
// (up to 10 per request). Cursor advancement only happens after FlushBatch
// returns, preserving CHK-01 durability.
//
// Use NewSQSSinkConsumer to construct — do not create directly.
type SQSSinkConsumer struct {
	id              string
	client          sqsAPI
	queueURL        string             // default queue URL (cfg.QueueURL); used when template absent
	queueURLT       *template.Template // nil when QueueURLTemplate is empty
	validatedQueues map[string]bool    // set of FIFO-validated queue URLs
	mu              sync.RWMutex       // protects validatedQueues and pending
	pending         map[uint32][]pendingSQSMessage // buffered messages for FlushBatch
	m               *observability.KaptantoMetrics
}

// Compile-time assertion: SQSSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*SQSSinkConsumer)(nil)

// NewSQSSinkConsumer creates a SQSSinkConsumer that publishes to the FIFO queue
// identified by cfg.QueueURL.
//
// It returns a non-nil error when:
//   - cfg.QueueURLTemplate is set but fails to parse as a Go template
//   - GetQueueAttributes fails (access denied, queue not found, network error)
//   - The queue is not a FIFO queue (FifoQueue attribute != "true")
//
// Static credentials are used when both cfg.AccessKeyID and cfg.SecretAccessKey
// are non-empty; otherwise the full AWS credential chain applies
// (env vars → ~/.aws/credentials → IAM instance profile).
func NewSQSSinkConsumer(id string, cfg config.SQSSinkConfig) (*SQSSinkConsumer, error) {
	// 0. Parse QueueURLTemplate before any AWS config loading — fail fast on bad templates.
	var queueURLT *template.Template
	if cfg.QueueURLTemplate != "" {
		t, err := template.New("queue-url").Parse(cfg.QueueURLTemplate)
		if err != nil {
			return nil, fmt.Errorf("sqs sink: queue-url-template parse error: %w", err)
		}
		queueURLT = t
	}

	// 1. Build AWS SDK option functions.
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	// Wire TLS transport when any TLS field is configured (CFG-03: CA pinning + mTLS).
	if cfg.TLS.CAFile != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" {
		// Guard: cert-file and key-file must both be set or both absent.
		if (cfg.TLS.CertFile != "") != (cfg.TLS.KeyFile != "") {
			return nil, fmt.Errorf("sqs sink: tls cert-file and key-file must both be set for mTLS")
		}

		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

		if cfg.TLS.CAFile != "" {
			pemData, err := os.ReadFile(cfg.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("sqs sink: read ca-file %q: %w", cfg.TLS.CAFile, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemData) {
				return nil, fmt.Errorf("sqs sink: ca-file %q: no valid PEM certificates found", cfg.TLS.CAFile)
			}
			tlsCfg.RootCAs = pool
		}

		if cfg.TLS.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("sqs sink: load client cert: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}

		opts = append(opts, awsconfig.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
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

	return newConsumerWithClient(id, cfg.QueueURL, client, queueURLT)
}

// newConsumerWithClient is the internal constructor used by both NewSQSSinkConsumer
// and tests. It validates that queueURL refers to a FIFO queue via GetQueueAttributes
// and seeds the validatedQueues pool with the default queue URL.
func newConsumerWithClient(id string, queueURL string, client sqsAPI, queueURLT *template.Template) (*SQSSinkConsumer, error) {
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
		id:              id,
		client:          client,
		queueURL:        queueURL,
		queueURLT:       queueURLT,
		validatedQueues: map[string]bool{queueURL: true},
		pending:         make(map[uint32][]pendingSQSMessage),
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
// It is the id argument passed to NewSQSSinkConsumer.
func (c *SQSSinkConsumer) ID() string {
	return c.id
}

// resolveQueueURL returns the target queue URL for the given log entry.
// When queueURLT is nil (no template configured), it returns the default queueURL.
// Otherwise it executes the template against entry.Event and trims whitespace.
// An error is returned if template execution fails or the result is empty.
func (c *SQSSinkConsumer) resolveQueueURL(entry eventlog.LogEntry) (string, error) {
	if c.queueURLT == nil {
		return c.queueURL, nil
	}
	var buf bytes.Buffer
	if err := c.queueURLT.Execute(&buf, entry.Event); err != nil {
		return "", fmt.Errorf("sqs sink: queue-url-template execution: %w", err)
	}
	url := strings.TrimSpace(buf.String())
	if url == "" {
		return "", fmt.Errorf("sqs sink: queue-url-template rendered to empty string — check QueueURLTemplate config")
	}
	return url, nil
}

// getOrValidateQueue ensures queueURL refers to a FIFO queue exactly once per
// unique URL. It uses double-checked locking: an RLock fast path avoids lock
// contention for already-validated queues; the write-lock slow path calls
// GetQueueAttributes and records the result.
func (c *SQSSinkConsumer) getOrValidateQueue(ctx context.Context, queueURL string) error {
	c.mu.RLock()
	ok := c.validatedQueues[queueURL]
	c.mu.RUnlock()
	if ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.validatedQueues[queueURL] {
		return nil
	}
	out, err := c.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue},
	})
	if err != nil {
		return fmt.Errorf("sqs sink: get queue attributes for %q: %w", queueURL, err)
	}
	if out.Attributes[string(types.QueueAttributeNameFifoQueue)] != "true" {
		return fmt.Errorf("sqs sink: %q is not a FIFO queue — SQS sink requires a .fifo queue URL", queueURL)
	}
	c.validatedQueues[queueURL] = true
	return nil
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *SQSSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver enqueues entry.Event into the consumer's pending buffer for batch
// publishing. No network I/O happens here; the actual publish is performed by
// FlushBatch, which is called by the Router once per ReadPartition batch.
//
// This amortises per-event SendMessage round-trips into SendMessageBatch calls
// (up to 10 messages per HTTPS request), significantly increasing throughput
// under sustained high eps. CHK-01 is preserved: the router only advances the
// cursor after FlushBatch returns nil.
//
// MessageGroupId is FNV-1a 64-bit hex of entry.Event.Key (16 chars, within
// SQS's 128-char limit) to preserve per-key ordering in the FIFO queue.
//
// MessageDeduplicationId is SHA-256[:64] of entry.Event.IdempotencyKey
// (64 chars, within SQS's 128-char limit) for content-based deduplication.
//
// On encoding error Deliver returns a non-nil error immediately; the
// RetryScheduler will block the key (DLV-03).
func (c *SQSSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 0. Resolve target queue URL (template path or default).
	targetURL, err := c.resolveQueueURL(entry)
	if err != nil {
		return err
	}
	if err := c.getOrValidateQueue(ctx, targetURL); err != nil {
		return err
	}

	// 1. MessageGroupId: FNV-1a 64-bit hash of Key bytes, formatted as 16 zero-padded hex chars.
	h := fnv.New64a()
	h.Write(entry.Event.Key)
	groupID := fmt.Sprintf("%016x", h.Sum64())

	// 2. MessageDeduplicationId: SHA-256 hex of IdempotencyKey, truncated to 64 chars.
	sum := sha256.Sum256([]byte(entry.Event.IdempotencyKey))
	dedupID := fmt.Sprintf("%x", sum)[:64]

	// 3. Marshal the event to JSON for the message body.
	data, marshalErr := json.Marshal(entry.Event)
	if marshalErr != nil {
		return fmt.Errorf("sqs sink: marshal event: %w", marshalErr)
	}

	// 4. Use a unique per-batch ID based on the dedupID prefix (max 80 alphanumeric chars).
	// We use the first 16 hex chars of the dedup ID to keep it short and unique within a batch.
	batchID := dedupID[:16]

	// 5. Append to pending buffer — FlushBatch performs the actual network call.
	c.mu.Lock()
	// Ensure the batch ID is unique within the current partition's pending slice. If a
	// collision occurs (two events with the same idempotency key prefix in the
	// same batch), append a counter suffix.
	finalID := batchID
	for i := 0; ; i++ {
		collision := false
		for _, p := range c.pending[entry.PartitionID] {
			if p.entry.Id != nil && *p.entry.Id == finalID {
				collision = true
				break
			}
		}
		if !collision {
			break
		}
		finalID = fmt.Sprintf("%s%x", batchID[:14], i)
	}
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingSQSMessage{
		queueURL: targetURL,
		entry: types.SendMessageBatchRequestEntry{
			Id:                     aws.String(finalID),
			MessageBody:            aws.String(string(data)),
			MessageGroupId:         aws.String(groupID),
			MessageDeduplicationId: aws.String(dedupID),
			MessageAttributes: map[string]types.MessageAttributeValue{
				"Kaptanto-Idempotency-Key": {
					DataType:    aws.String("String"),
					StringValue: aws.String(entry.Event.IdempotencyKey),
				},
			},
		},
	})
	c.mu.Unlock()
	return nil
}

// FlushBatch publishes all buffered messages via SendMessageBatch (≤10 per
// request). Messages are grouped by target queue URL and chunked into batches
// of at most 10 (the SQS API limit). All batch entries in a chunk must succeed;
// any per-entry failure causes FlushBatch to return an error so the Router's
// RetryScheduler can block the affected key groups.
//
// CHK-01 is preserved: the router only advances the cursor after FlushBatch
// returns nil for the entire pending set.
func (c *SQSSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.mu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	c.mu.Unlock()

	// Group messages by target queue URL.
	byQueue := make(map[string][]types.SendMessageBatchRequestEntry)
	for _, pm := range batch {
		byQueue[pm.queueURL] = append(byQueue[pm.queueURL], pm.entry)
	}

	start := time.Now()
	var firstErr error

	for queueURL, entries := range byQueue {
		// Chunk into batches of at most 10 (SQS API limit).
		for i := 0; i < len(entries); i += 10 {
			end := i + 10
			if end > len(entries) {
				end = len(entries)
			}
			chunk := entries[i:end]

			out, sendErr := c.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
				QueueUrl: aws.String(queueURL),
				Entries:  chunk,
			})

			if c.m != nil {
				c.m.QueuePublishLatency.WithLabelValues("sqs").Observe(time.Since(start).Seconds())
			}

			if sendErr != nil {
				if c.m != nil {
					c.m.QueuePublishErrors.WithLabelValues("sqs").Add(float64(len(chunk)))
				}
				if firstErr == nil {
					firstErr = fmt.Errorf("sqs sink: send batch to %q: %w", queueURL, sendErr)
				}
				continue
			}

			// Record per-entry failures from the batch response.
			if out != nil && len(out.Failed) > 0 {
				if c.m != nil {
					c.m.QueuePublishErrors.WithLabelValues("sqs").Add(float64(len(out.Failed)))
				}
				if firstErr == nil {
					failed := out.Failed[0]
					msg := "unknown"
					if failed.Message != nil {
						msg = *failed.Message
					}
					code := ""
					if failed.Code != nil {
						code = *failed.Code
					}
					firstErr = fmt.Errorf("sqs sink: batch entry failed on %q: code=%s message=%s", queueURL, code, msg)
				}
			}
			if c.m != nil {
				c.m.QueuePublishTotal.WithLabelValues("sqs").Add(float64(len(chunk) - len(out.Failed)))
			}
		}
	}

	return firstErr
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
// connection pooling internally. validatedQueues holds only strings — no
// stateful resources to drain.
func (c *SQSSinkConsumer) Close() {
	// no-op: SQS uses stateless HTTP; AWS SDK manages connection pooling internally.
	// validatedQueues holds only strings — no stateful resources to drain.
}
