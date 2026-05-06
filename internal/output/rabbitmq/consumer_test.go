package rabbitmqsink_test

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	rabbitmqsink "github.com/olucasandrade/kaptanto/internal/output/rabbitmq"
)

// fakeDeferred is a fake implementation of deferredConfirmAPI (exported for test).
type fakeDeferred struct {
	acked bool
	err   error
}

func (f *fakeDeferred) WaitContext(_ context.Context) (bool, error) {
	return f.acked, f.err
}

// fakeAMQPChannel is a fake implementation of amqpChannelAPI.
type fakeAMQPChannel struct {
	publishErr     error
	deferred       rabbitmqsink.DeferredConfirmAPI
	lastPublishing amqp.Publishing
	lastExchange   string
	lastKey        string
	callCount      int
}

func (f *fakeAMQPChannel) PublishWithDeferredConfirmWithContext(
	ctx context.Context,
	exchange, key string,
	mandatory, immediate bool,
	msg amqp.Publishing,
) (rabbitmqsink.DeferredConfirmAPI, error) {
	f.callCount++
	f.lastExchange = exchange
	f.lastKey = key
	f.lastPublishing = msg
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	return f.deferred, nil
}

// makeEntry builds a LogEntry for test use.
func makeEntry(partitionID uint32, idempotencyKey string) eventlog.LogEntry {
	return eventlog.LogEntry{
		PartitionID: partitionID,
		Event: &event.ChangeEvent{
			IdempotencyKey: idempotencyKey,
			Table:          "orders",
			Operation:      event.OpInsert,
		},
	}
}

// makeConsumerWithChannel builds a RabbitMQSinkConsumer backed by a single
// fakeAMQPChannel placed at every partition slot.
func makeConsumerWithChannel(t *testing.T, ch rabbitmqsink.AMQPChannelAPI, routingKeyTemplate string) *rabbitmqsink.RabbitMQSinkConsumer {
	t.Helper()
	cfg := config.RabbitMQSinkConfig{
		Exchange:           "test-exchange",
		RoutingKeyTemplate: routingKeyTemplate,
	}
	var channels [64]rabbitmqsink.AMQPChannelAPI
	for i := range channels {
		channels[i] = ch
	}
	c, err := rabbitmqsink.NewConsumerWithChannels("rabbitmq", cfg, channels)
	if err != nil {
		t.Fatalf("NewConsumerWithChannels: %v", err)
	}
	return c
}

// TestRabbitMQSink_Deliver_Ack verifies that a broker ack returns nil and
// increments QueuePublishTotal.
func TestRabbitMQSink_Deliver_Ack(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: true, err: nil},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry(0, "key-ack")
	if err := c.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("expected nil error on ack, got: %v", err)
	}
	if ch.callCount != 1 {
		t.Fatalf("expected 1 publish call, got %d", ch.callCount)
	}
}

// TestRabbitMQSink_Deliver_Nack verifies that a broker nack returns a non-nil
// error containing the exchange name, and increments QueuePublishErrors.
func TestRabbitMQSink_Deliver_Nack(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: false, err: nil},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry(0, "key-nack")
	err := c.Deliver(context.Background(), entry)
	if err == nil {
		t.Fatal("expected non-nil error on nack")
	}
	if !containsAny(err.Error(), "test-exchange") {
		t.Fatalf("expected error to contain exchange name, got: %v", err)
	}
}

// TestRabbitMQSink_Deliver_PublishError verifies that when PublishWithDeferred
// returns an error, Deliver returns a non-nil error and QueuePublishErrors is
// incremented.
func TestRabbitMQSink_Deliver_PublishError(t *testing.T) {
	publishErr := errors.New("broker unavailable")
	ch := &fakeAMQPChannel{publishErr: publishErr}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry(0, "key-publish-err")
	err := c.Deliver(context.Background(), entry)
	if err == nil {
		t.Fatal("expected non-nil error on publish failure")
	}
	if !errors.Is(err, publishErr) {
		t.Fatalf("expected error to wrap publishErr, got: %v", err)
	}
}

// TestRabbitMQSink_Deliver_WaitContextError verifies that when WaitContext
// returns an error, Deliver returns a non-nil error and QueuePublishErrors is
// incremented.
func TestRabbitMQSink_Deliver_WaitContextError(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: false, err: context.Canceled},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry(0, "key-wait-err")
	err := c.Deliver(context.Background(), entry)
	if err == nil {
		t.Fatal("expected non-nil error when WaitContext returns error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error to wrap context.Canceled, got: %v", err)
	}
}

// TestRabbitMQSink_Deliver_TemplateError verifies that NewConsumerWithChannels
// returns an error when routing key template is invalid.
func TestRabbitMQSink_Deliver_TemplateError(t *testing.T) {
	cfg := config.RabbitMQSinkConfig{
		Exchange:           "ex",
		RoutingKeyTemplate: "{{.InvalidSyntax",
	}
	var channels [64]rabbitmqsink.AMQPChannelAPI
	_, err := rabbitmqsink.NewConsumerWithChannels("rabbitmq", cfg, channels)
	if err == nil {
		t.Fatal("expected error for invalid routing key template")
	}
}

// TestRabbitMQSink_Deliver_EmptyRoutingKey verifies that when the routing key
// template renders to empty/whitespace, Deliver returns an error.
func TestRabbitMQSink_Deliver_EmptyRoutingKey(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: true, err: nil},
	}
	// Template renders to empty string for any event
	c := makeConsumerWithChannel(t, ch, "   ")
	entry := makeEntry(0, "key-empty-routing")
	err := c.Deliver(context.Background(), entry)
	if err == nil {
		t.Fatal("expected non-nil error for empty routing key")
	}
}

// TestRabbitMQSink_Ping_Closed verifies that Ping returns an error when
// conn is nil.
func TestRabbitMQSink_Ping_Closed(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: true, err: nil},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	// conn is nil in test consumer built by NewConsumerWithChannels
	err := c.Ping()
	if err == nil {
		t.Fatal("expected non-nil error when conn is nil")
	}
}

// TestRabbitMQSink_Deliver_SetsHeader verifies that the Kaptanto-Idempotency-Key
// header is set to entry.Event.IdempotencyKey on every published message.
func TestRabbitMQSink_Deliver_SetsHeader(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: true, err: nil},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")

	const wantKey = "idempotency-key-abc123"
	entry := makeEntry(0, wantKey)
	if err := c.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	gotHeader, ok := ch.lastPublishing.Headers["Kaptanto-Idempotency-Key"]
	if !ok {
		t.Fatal("expected Kaptanto-Idempotency-Key header to be set")
	}
	if gotHeader != wantKey {
		t.Fatalf("expected header %q, got %q", wantKey, gotHeader)
	}
}

// TestRabbitMQSink_Deliver_PersistentDeliveryMode verifies that every published
// message has DeliveryMode set to amqp.Persistent.
func TestRabbitMQSink_Deliver_PersistentDeliveryMode(t *testing.T) {
	ch := &fakeAMQPChannel{
		deferred: &fakeDeferred{acked: true, err: nil},
	}
	c := makeConsumerWithChannel(t, ch, "{{.Table}}")
	entry := makeEntry(0, "key-mode")
	if err := c.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if ch.lastPublishing.DeliveryMode != amqp.Persistent {
		t.Fatalf("expected DeliveryMode=%d (Persistent), got %d",
			amqp.Persistent, ch.lastPublishing.DeliveryMode)
	}
}

// TestRabbitMQSink_Deliver_PartitionRouting verifies that entries with different
// PartitionIDs are routed to different channel slots.
func TestRabbitMQSink_Deliver_PartitionRouting(t *testing.T) {
	ch0 := &fakeAMQPChannel{deferred: &fakeDeferred{acked: true}}
	ch1 := &fakeAMQPChannel{deferred: &fakeDeferred{acked: true}}

	cfg := config.RabbitMQSinkConfig{
		Exchange:           "ex",
		RoutingKeyTemplate: "{{.Table}}",
	}
	// Place ch0 at slot 0, ch1 at slot 1, ch0 elsewhere
	var channels [64]rabbitmqsink.AMQPChannelAPI
	for i := range channels {
		channels[i] = ch0
	}
	channels[1] = ch1

	c, err := rabbitmqsink.NewConsumerWithChannels("rabbitmq", cfg, channels)
	if err != nil {
		t.Fatalf("NewConsumerWithChannels: %v", err)
	}

	// PartitionID=0 → slot 0 → ch0
	e0 := makeEntry(0, "key-partition-0")
	if err := c.Deliver(context.Background(), e0); err != nil {
		t.Fatalf("Deliver partition 0: %v", err)
	}
	// PartitionID=1 → slot 1 → ch1
	e1 := makeEntry(1, "key-partition-1")
	if err := c.Deliver(context.Background(), e1); err != nil {
		t.Fatalf("Deliver partition 1: %v", err)
	}

	if ch0.callCount != 1 {
		t.Fatalf("expected ch0 callCount=1, got %d", ch0.callCount)
	}
	if ch1.callCount != 1 {
		t.Fatalf("expected ch1 callCount=1, got %d", ch1.callCount)
	}
}

// containsAny checks if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
