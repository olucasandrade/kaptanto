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
package pubsubsink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// PubSubSinkConsumer is a router.Consumer that publishes CDC events to Google Cloud
// Pub/Sub using the pubsub/v2 client library with synchronous result.Get confirmation (CHK-01).
//
// Use NewPubSubSinkConsumer to construct — do not create directly.
type PubSubSinkConsumer struct {
	id        string
	client    *pubsub.Client
	publisher *pubsub.Publisher
	projectID string
	topicID   string
	topicT    *template.Template // nil when TopicTemplate is empty
	m         *observability.KaptantoMetrics
}

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

	// 4. Create the publisher for the configured topic (v2 API: Publisher, not Topic).
	publisher := client.Publisher(cfg.TopicID)

	// 5. Enable message ordering BEFORE any Publish call (required for OrderingKey support).
	publisher.EnableMessageOrdering = true

	return &PubSubSinkConsumer{
		id:        id,
		client:    client,
		publisher: publisher,
		projectID: cfg.ProjectID,
		topicID:   cfg.TopicID,
		topicT:    topicT,
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

// Deliver publishes entry.Event to Pub/Sub synchronously using result.Get(ctx) (CHK-01).
//
// It blocks until the Pub/Sub server acknowledges the publish.
// The router's cursor is NOT advanced until this function returns nil.
//
// The Pub/Sub message is built as follows:
//   - Data:        JSON-marshalled entry.Event
//   - OrderingKey: string(entry.Event.Key) — the CDC primary key (DLV-02)
//   - Attributes:  {"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey} (DLV-04)
//
// Note on TopicTemplate: the Pub/Sub publisher is created for a fixed topicID at
// construction time (unlike Kafka where topic is passed per-record). TopicTemplate
// is preserved in the config for future multi-topic support but is not applied per-message
// in v2.6.0. Messages are always published to the configured TopicID.
//
// On error, Deliver calls ResumePublish if ErrPublishingPaused is detected, then returns
// a non-nil error. The RetryScheduler is responsible for rescheduling; Deliver never
// retries internally (DLV-03).
func (c *PubSubSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Resolve ordering key: string(entry.Event.Key).
	orderingKey := string(entry.Event.Key)

	// 2. Marshal the event to JSON for the message data.
	data, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("pubsub sink: marshal event: %w", err)
	}

	// 3. Publish the message; Publish is non-blocking — it returns a PublishResult.
	start := time.Now()
	result := c.publisher.Publish(ctx, &pubsub.Message{
		Data:        data,
		OrderingKey: orderingKey,
		Attributes: map[string]string{
			"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey,
		},
	})

	// 4. Block until the server acknowledges (CHK-01 — cursor does not advance before ack).
	_, publishErr := result.Get(ctx)

	// 5. Observe publish latency regardless of outcome.
	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("pubsub").Observe(time.Since(start).Seconds())
	}

	// 6. Handle publish error.
	if publishErr != nil {
		// If ordering-key publishing is paused due to a previous error, resume it
		// before returning so the next Deliver call can proceed (not permanently blocked).
		var paused pubsub.ErrPublishingPaused
		if errors.As(publishErr, &paused) {
			c.publisher.ResumePublish(paused.OrderingKey)
		}
		if c.m != nil {
			c.m.QueuePublishErrors.WithLabelValues("pubsub").Inc()
		}
		return fmt.Errorf("pubsub sink: publish for key %q: %w", orderingKey, publishErr)
	}

	// 7. Success — increment total counter.
	if c.m != nil {
		c.m.QueuePublishTotal.WithLabelValues("pubsub").Inc()
	}
	return nil
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

// Close stops the publisher (draining buffered messages) and closes the gRPC
// connection pool. Always call publisher.Stop() before client.Close().
// Close is safe to call multiple times.
func (c *PubSubSinkConsumer) Close() {
	c.publisher.Stop() // drain buffered messages first
	c.client.Close()   // then close gRPC connection pool
}
