// Package kafkasink provides KafkaSinkConsumer, a router.Consumer implementation
// that publishes CDC events to an Apache Kafka cluster using franz-go.
//
// Key design decisions:
//   - CHK-01 (Durability): Deliver calls ProduceSync, which blocks until all
//     in-sync replicas have acknowledged the write. The router's cursor is NOT
//     advanced until ProduceSync returns nil, preserving at-least-once delivery.
//   - DLV-02 (Per-key ordering): Record.Key is set to entry.Event.Key (the CDC
//     primary key bytes). Kafka routes records with the same key to the same
//     partition, giving per-key ordering within a topic.
//   - DLV-04 (Idempotency header): Every record carries a "Kaptanto-Idempotency-Key"
//     header set to entry.Event.IdempotencyKey, enabling downstream deduplication.
//   - DLV-03 (No internal retry): On ProduceSync failure Deliver returns a non-nil
//     error immediately. Retry is the RetryScheduler's responsibility.
//   - CGO-free: franz-go is a pure Go Kafka client; CGO_ENABLED=0 is safe.
package kafkasink

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertion: KafkaSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*KafkaSinkConsumer)(nil)

// KafkaSinkConsumer is a router.Consumer that publishes CDC events to an Apache
// Kafka cluster using franz-go's produce API.
//
// When used with the Router's BatchFlusher interface, Deliver enqueues records
// into a per-consumer buffer and FlushBatch calls Produce for all pending
// records then awaits acks via a single Flush call. This avoids the per-record
// ProduceSync RTT that defeats franz-go's internal batching. CHK-01 is
// preserved: the router only advances the cursor after FlushBatch returns.
//
// Use NewKafkaSinkConsumer to construct — do not create directly.
type KafkaSinkConsumer struct {
	id      string
	client  *kgo.Client
	topicT  *template.Template
	mu      sync.Mutex
	pending map[uint32][]*kgo.Record
	m       *observability.KaptantoMetrics
}

// Compile-time assertion: KafkaSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*KafkaSinkConsumer)(nil)

// NewKafkaSinkConsumer creates a KafkaSinkConsumer connected to cfg.BootstrapServers.
//
// It returns a non-nil error when:
//   - cfg.TopicTemplate is not a valid Go template
//   - cfg.SASLMechanism is non-empty but not one of "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"
//   - TLS certificate files are specified but cannot be read or parsed
//   - The kgo.Client cannot be constructed (e.g. no brokers reachable at startup)
//
// SASL mechanism values are case-sensitive: use uppercase ("PLAIN", not "plain").
// The caller is responsible for calling Close() when done.
func NewKafkaSinkConsumer(id string, cfg config.KafkaSinkConfig) (*KafkaSinkConsumer, error) {
	// 1. Parse the topic template early so template errors are caught at startup.
	tmpl, err := template.New("topic").Parse(cfg.TopicTemplate)
	if err != nil {
		return nil, fmt.Errorf("kafka sink: topic template parse error: %w", err)
	}

	// 2. Assemble kgo client options.
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.BootstrapServers...),
		kgo.RequiredAcks(kgo.AllISRAcks()), // CHK-01: wait for all ISR acks
	}

	// 3. SASL: add mechanism option only when SASLMechanism is configured.
	if cfg.SASLMechanism != "" {
		mechanism, err := buildSASLMechanism(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.SASL(mechanism))
	}

	// 4. TLS: add DialTLSConfig when any TLS file is specified.
	if cfg.TLS.CAFile != "" || cfg.TLS.CertFile != "" {
		tlsCfg, err := buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.DialTLSConfig(tlsCfg))
	}

	// 5. Construct the kgo.Client.
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka sink: create client: %w", err)
	}

	return &KafkaSinkConsumer{
		id:      id,
		client:  client,
		topicT:  tmpl,
		pending: make(map[uint32][]*kgo.Record),
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
// It is the id argument passed to NewKafkaSinkConsumer.
func (c *KafkaSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *KafkaSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver enqueues entry.Event into the consumer's pending buffer for batch
// publishing. No network I/O happens here; the actual produce is performed by
// FlushBatch, which is called by the Router once per ReadPartition batch.
//
// This allows franz-go to form optimal batch payloads via Produce+Flush rather
// than per-record ProduceSync, significantly increasing throughput under
// sustained high eps. CHK-01 is preserved: the router only advances the
// cursor after FlushBatch returns nil.
//
// The Kafka record is built as follows:
//   - Topic: derived from TopicTemplate executed against entry.Event
//   - Key:   entry.Event.Key (json.RawMessage — direct []byte assignment, DLV-02)
//   - Value: JSON-marshalled entry.Event
//   - Headers: [{"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey}] (DLV-04)
//
// On encoding error Deliver returns a non-nil error immediately; the
// RetryScheduler will block the key (DLV-03).
func (c *KafkaSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Derive topic from template.
	var buf bytes.Buffer
	if err := c.topicT.Execute(&buf, entry.Event); err != nil {
		return fmt.Errorf("kafka sink: topic template execution: %w", err)
	}
	topic := strings.TrimSpace(buf.String())

	// 2. Validate the derived topic is non-empty (Pitfall 4: empty topic string).
	if topic == "" {
		return fmt.Errorf("kafka sink: topic template rendered to an empty string — check TopicTemplate config")
	}

	// 3. Marshal the event to JSON for the record value.
	data, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("kafka sink: marshal event: %w", err)
	}

	// 4. Build the Kafka record.
	record := &kgo.Record{
		Topic: topic,
		Key:   entry.Event.Key,
		Value: data,
		Headers: []kgo.RecordHeader{
			{Key: "Kaptanto-Idempotency-Key", Value: []byte(entry.Event.IdempotencyKey)},
		},
	}

	// 5. Append to pending buffer — FlushBatch performs the actual network call.
	c.mu.Lock()
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], record)
	c.mu.Unlock()
	return nil
}

// FlushBatch produces all buffered records via async Produce calls, then calls
// Flush to wait for all broker acks. This lets franz-go form optimal batch
// payloads rather than issuing one ProduceSync per record.
//
// CHK-01 is preserved: the router only advances the cursor after FlushBatch
// returns nil for the entire pending set.
func (c *KafkaSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.mu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	c.mu.Unlock()

	// Collect async produce errors via a channel (one slot per record).
	errCh := make(chan error, len(batch))
	start := time.Now()

	for _, rec := range batch {
		r := rec // capture loop variable
		c.client.Produce(ctx, r, func(rec *kgo.Record, err error) {
			errCh <- err
		})
	}

	// Flush waits until all buffered Produce calls have been sent and acked.
	flushErr := c.client.Flush(ctx)

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("kafka").Observe(time.Since(start).Seconds())
	}

	if flushErr != nil {
		if c.m != nil {
			c.m.QueuePublishErrors.WithLabelValues("kafka").Add(float64(len(batch)))
		}
		return fmt.Errorf("kafka sink: flush batch: %w", flushErr)
	}

	// Drain per-record callbacks.
	var firstErr error
	successCount := 0
	for range batch {
		if err := <-errCh; err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("kafka sink: produce record: %w", err)
			}
		} else {
			successCount++
		}
	}

	if c.m != nil {
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("kafka").Add(float64(successCount))
		}
		if firstErr != nil {
			c.m.QueuePublishErrors.WithLabelValues("kafka").Add(float64(len(batch) - successCount))
		}
	}
	return firstErr
}

// Ping verifies the Kafka cluster is reachable by issuing a Metadata request.
// It uses a 5-second timeout and returns nil when the broker responds,
// or a non-nil error when the cluster is unreachable.
func (c *KafkaSinkConsumer) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.client.Ping(ctx); err != nil {
		return fmt.Errorf("kafka sink: ping: %w", err)
	}
	return nil
}

// Close drains pending produce requests and closes all TCP connections to the
// Kafka cluster. It is safe to call Close multiple times.
func (c *KafkaSinkConsumer) Close() {
	c.client.Close()
}

// buildSASLMechanism returns the sasl.Mechanism for cfg.SASLMechanism.
// Supported values (case-sensitive): "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512".
// Returns an error for any other value — values must be uppercase as documented
// in KafkaSinkConfig; lower-case input is NOT silently normalised.
func buildSASLMechanism(cfg config.KafkaSinkConfig) (sasl.Mechanism, error) {
	switch cfg.SASLMechanism {
	case "PLAIN":
		return plain.Auth{
			User: cfg.SASLUsername,
			Pass: cfg.SASLPassword,
		}.AsMechanism(), nil

	case "SCRAM-SHA-256":
		return scram.Auth{
			User: cfg.SASLUsername,
			Pass: cfg.SASLPassword,
		}.AsSha256Mechanism(), nil

	case "SCRAM-SHA-512":
		return scram.Auth{
			User: cfg.SASLUsername,
			Pass: cfg.SASLPassword,
		}.AsSha512Mechanism(), nil

	default:
		return nil, fmt.Errorf(
			"kafka sink: unknown sasl-mechanism %q — must be one of PLAIN, SCRAM-SHA-256, SCRAM-SHA-512",
			cfg.SASLMechanism,
		)
	}
}

// buildTLSConfig constructs a *tls.Config from cfg:
//   - If CAFile is set, loads the CA certificate pool from the file.
//   - If CertFile and KeyFile are both set, loads the client key pair for mTLS.
func buildTLSConfig(tlsCfg config.TLSConfig) (*tls.Config, error) {
	cfg := &tls.Config{}

	if tlsCfg.CAFile != "" {
		pem, err := os.ReadFile(tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("kafka sink: read ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("kafka sink: no valid certs in ca-file %q", tlsCfg.CAFile)
		}
		cfg.RootCAs = pool
	}

	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("kafka sink: load client cert: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
