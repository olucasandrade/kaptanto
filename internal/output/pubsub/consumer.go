// Package pubsubsink provides PubSubSinkConsumer, a router.Consumer implementation
// that publishes CDC events to Google Cloud Pub/Sub using the pubsub/v2 client library.
//
// Key design decisions:
//   - CHK-01 (Durability): Deliver calls result.Get(ctx), which blocks until the
//     Pub/Sub server acknowledges the publish. The router's cursor is NOT advanced
//     until result.Get returns nil, preserving at-least-once delivery.
//   - DLV-02 (Per-key ordering): OrderingKey is set to string(entry.Event.Key) (the CDC
//     primary key bytes). EnableMessageOrdering=true routes messages with the same key
//     to the same ordering group, giving per-key ordering.
//   - DLV-04 (Idempotency attribute): Every message carries a "Kaptanto-Idempotency-Key"
//     attribute set to entry.Event.IdempotencyKey, enabling downstream deduplication.
//   - DLV-03 (No internal retry): On publish failure, Deliver calls ResumePublish if the
//     error is ErrPublishingPaused, then returns a non-nil error immediately. Retry is
//     the RetryScheduler's responsibility.
//   - CGO-free: cloud.google.com/go/pubsub/v2 is a pure Go client; CGO_ENABLED=0 is safe.
//   - Per-table topic routing: When TopicTemplate is set, Deliver evaluates the template
//     against entry.Event per-message and routes to the resolved topic's publisher. A lazy
//     publisher pool (map[string]*pubsub.Publisher, protected by sync.RWMutex) creates
//     publishers on first access and shuts them all down on Close().
package pubsubsink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/api/option"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertion: PubSubSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*PubSubSinkConsumer)(nil)

// pendingPubSubMessage holds a pending Pub/Sub PublishResult for batch ack collection.
type pendingPubSubMessage struct {
	result     *pubsub.PublishResult
	orderingKey string
	topicID    string
}

// PubSubSinkConsumer is a router.Consumer that publishes CDC events to Google Cloud
// Pub/Sub using the pubsub/v2 client library.
//
// When used with the Router's BatchFlusher interface, Deliver calls Publish
// (non-blocking) and stores the PublishResult; FlushBatch collects all results
// via result.Get concurrently. This amortises per-event Get round-trips into
// a single wait-for-all. CHK-01 is preserved: the router only advances the
// cursor after FlushBatch returns nil.
//
// Use NewPubSubSinkConsumer to construct — do not create directly.
type PubSubSinkConsumer struct {
	id         string
	client     *pubsub.Client
	publishers map[string]*pubsub.Publisher // keyed by resolved topic ID
	mu         sync.RWMutex                 // protects publishers map and pending
	projectID  string
	topicID    string                         // default topic (cfg.TopicID)
	topicT     *template.Template             // nil when TopicTemplate is empty
	pending    []pendingPubSubMessage
	m          *observability.KaptantoMetrics
}

// Compile-time assertion: PubSubSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*PubSubSinkConsumer)(nil)

// NewPubSubSinkConsumer creates a PubSubSinkConsumer that publishes to cfg.TopicID in cfg.ProjectID.
//
// clientOpts are passed directly to pubsub.NewClient; this allows tests to inject a
// pstest gRPC connection via option.WithGRPCConn without real GCP credentials.
//
// It returns a non-nil error when:
//   - cfg.TopicTemplate is non-empty but not a valid Go template
//   - pubsub.NewClient fails (e.g., invalid credentials file)
//
// The caller is responsible for calling Close() when done.
func NewPubSubSinkConsumer(id string, cfg config.PubSubSinkConfig, clientOpts ...option.ClientOption) (*PubSubSinkConsumer, error) {
	// 1. Parse TopicTemplate early; catch template errors at startup.
	var topicT *template.Template
	if cfg.TopicTemplate != "" {
		t, err := template.New("topic").Parse(cfg.TopicTemplate)
		if err != nil {
			return nil, fmt.Errorf("pubsub sink: topic template parse error: %w", err)
		}
		topicT = t
	}

	// 2. Build client options: add CredentialsFile if specified (else ADC is used).
	if cfg.CredentialsFile != "" {
		clientOpts = append(clientOpts, option.WithCredentialsFile(cfg.CredentialsFile))
	}

	// 3. Create the Pub/Sub client.
	client, err := pubsub.NewClient(context.Background(), cfg.ProjectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("pubsub sink: create client: %w", err)
	}

	// 4. Create the default publisher for cfg.TopicID and seed the pool.
	defaultPub := client.Publisher(cfg.TopicID)

	// 5. Enable message ordering BEFORE any Publish call (required for OrderingKey support).
	defaultPub.EnableMessageOrdering = true

	return &PubSubSinkConsumer{
		id:         id,
		client:     client,
		publishers: map[string]*pubsub.Publisher{cfg.TopicID: defaultPub},
		projectID:  cfg.ProjectID,
		topicID:    cfg.TopicID,
		topicT:     topicT,
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
// It is the id argument passed to NewPubSubSinkConsumer.
func (c *PubSubSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *PubSubSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// resolveTopicID returns the target topic ID for the given log entry.
// When TopicTemplate is set, it executes the template against entry.Event.
// When TopicTemplate is empty (topicT is nil), it returns cfg.TopicID directly.
// The router guarantees entry.Event is non-nil.
func (c *PubSubSinkConsumer) resolveTopicID(entry eventlog.LogEntry) (string, error) {
	if c.topicT == nil {
		return c.topicID, nil
	}
	var buf bytes.Buffer
	if err := c.topicT.Execute(&buf, entry.Event); err != nil {
		return "", fmt.Errorf("pubsub sink: topic template execution: %w", err)
	}
	topic := strings.TrimSpace(buf.String())
	if topic == "" {
		return "", fmt.Errorf("pubsub sink: topic template rendered to empty string — check TopicTemplate config")
	}
	return topic, nil
}

// getOrCreatePublisher returns the publisher for topicID from the pool, creating it
// lazily on first access. Uses double-checked lazy initialization under sync.RWMutex.
// EnableMessageOrdering is set immediately after creation (required for OrderingKey support).
func (c *PubSubSinkConsumer) getOrCreatePublisher(topicID string) *pubsub.Publisher {
	// Fast path: topic already in pool.
	c.mu.RLock()
	pub, ok := c.publishers[topicID]
	c.mu.RUnlock()
	if ok {
		return pub
	}
	// Slow path: first Deliver to this topic — create and register publisher.
	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check: another goroutine may have created it between RUnlock and Lock.
	if pub, ok = c.publishers[topicID]; ok {
		return pub
	}
	pub = c.client.Publisher(topicID)
	pub.EnableMessageOrdering = true
	c.publishers[topicID] = pub
	return pub
}

// Deliver enqueues entry.Event into the consumer's pending buffer by calling
// the non-blocking Publish and storing the PublishResult. No blocking ack wait
// happens here; FlushBatch collects all results via Get concurrently.
//
// This amortises per-event result.Get round-trips by pipelining Publish calls
// before waiting for acks. CHK-01 is preserved: the router only advances the
// cursor after FlushBatch returns nil.
//
// The Pub/Sub message is built as follows:
//   - Data:        JSON-marshalled entry.Event
//   - OrderingKey: string(entry.Event.Key) — the CDC primary key (DLV-02)
//   - Attributes:  {"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey} (DLV-04)
//
// On encoding error Deliver returns a non-nil error immediately; the
// RetryScheduler will block the key (DLV-03).
func (c *PubSubSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Resolve ordering key: string(entry.Event.Key).
	orderingKey := string(entry.Event.Key)

	// 2. Marshal the event to JSON for the message data.
	data, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("pubsub sink: marshal event: %w", err)
	}

	// 3. Resolve the target topic ID for this message.
	topicID, err := c.resolveTopicID(entry)
	if err != nil {
		return err
	}

	// 4. Get or lazily create the publisher for the resolved topic.
	pub := c.getOrCreatePublisher(topicID)

	// 5. Publish the message non-blocking — store the result for FlushBatch.
	result := pub.Publish(ctx, &pubsub.Message{
		Data:        data,
		OrderingKey: orderingKey,
		Attributes: map[string]string{
			"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey,
		},
	})

	// 6. Append result to pending buffer — FlushBatch awaits all acks.
	c.mu.Lock()
	c.pending = append(c.pending, pendingPubSubMessage{
		result:      result,
		orderingKey: orderingKey,
		topicID:     topicID,
	})
	c.mu.Unlock()
	return nil
}

// FlushBatch awaits all buffered PublishResults via result.Get. This collects
// acks for all messages published during the current batch without per-message
// round-trip serialisation.
//
// If any result reports ErrPublishingPaused, ResumePublish is called on the
// affected ordering key before the error is returned.
//
// CHK-01 is preserved: the router only advances the cursor after FlushBatch
// returns nil for the entire pending set.
func (c *PubSubSinkConsumer) FlushBatch(ctx context.Context) error {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending
	c.pending = nil
	c.mu.Unlock()

	start := time.Now()
	var firstErr error
	successCount := 0

	for _, pm := range batch {
		_, publishErr := pm.result.Get(ctx)
		if publishErr != nil {
			// Resume paused ordering key so subsequent Deliver calls can proceed.
			pub := c.getOrCreatePublisher(pm.topicID)
			var paused pubsub.ErrPublishingPaused
			if errors.As(publishErr, &paused) {
				pub.ResumePublish(paused.OrderingKey)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("pubsub sink: publish for key %q to topic %q: %w", pm.orderingKey, pm.topicID, publishErr)
			}
		} else {
			successCount++
		}
	}

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("pubsub").Observe(time.Since(start).Seconds())
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("pubsub").Add(float64(successCount))
		}
		if firstErr != nil {
			c.m.QueuePublishErrors.WithLabelValues("pubsub").Add(float64(len(batch) - successCount))
		}
	}
	return firstErr
}

// Ping verifies the configured Pub/Sub topic is reachable by issuing a GetTopic RPC.
// It uses a 5-second timeout and returns nil when the topic exists and is reachable.
func (c *PubSubSinkConsumer) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.client.TopicAdminClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{
		Topic: fmt.Sprintf("projects/%s/topics/%s", c.projectID, c.topicID),
	})
	if err != nil {
		return fmt.Errorf("pubsub sink: ping topic %q: %w", c.topicID, err)
	}
	return nil
}

// Close stops all publishers in the pool (draining buffered messages) and closes
// the gRPC connection pool. The publisher map is snapshotted under lock before
// calling Stop() outside the lock to avoid deadlock with in-flight Deliver goroutines.
// Always call Stop() on all publishers before client.Close().
func (c *PubSubSinkConsumer) Close() {
	c.mu.Lock()
	pubs := make([]*pubsub.Publisher, 0, len(c.publishers))
	for _, pub := range c.publishers {
		pubs = append(pubs, pub)
	}
	c.mu.Unlock() // Release lock before Stop() to avoid deadlock with in-flight Deliver goroutines.

	for _, pub := range pubs {
		pub.Stop() // blocks until buffered messages are sent or publisher fails
	}
	c.client.Close() // then close the shared gRPC connection pool
}
