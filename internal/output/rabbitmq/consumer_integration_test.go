package rabbitmqsink_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	rabbitmqsink "github.com/olucasandrade/kaptanto/internal/output/rabbitmq"
)

// rabbitMQTestURL returns the live-broker URL from RABBITMQ_TEST_URL, or
// skips the test. Set it to a running RabbitMQ instance to enable, e.g.:
//
//	RABBITMQ_TEST_URL="amqp://guest:guest@localhost:5672/" go test ./internal/output/rabbitmq/... -run TestRabbitMQIntegration -v
//
// The integration workflow (.github/workflows/integration.yml) provisions a
// rabbitmq:3 container and exports RABBITMQ_TEST_URL.
func rabbitMQTestURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("set RABBITMQ_TEST_URL (e.g. amqp://guest:guest@localhost:5672/) to run RabbitMQ integration tests")
	}
	return url
}

func TestRabbitMQPreflight_Dial(t *testing.T) {
	url := rabbitMQTestURL(t)
	conn, err := amqp.Dial(url)
	require.NoError(t, err, "RabbitMQ broker must accept amqp.Dial before integration suite runs")
	require.NoError(t, conn.Close())
}

// TestIntegrationCanary_RABBITMQ_TEST_URL fails the build when running under
// GitHub Actions and RABBITMQ_TEST_URL is unset. A green integration job
// does not, by itself, prove that TestRabbitMQIntegration_RoundTrip actually
// ran — it proves only that nothing failed, which is equally true whether
// the test ran or silently t.Skip()'d. This canary turns that silent skip
// into a hard CI failure.
//
// It must never fire outside CI: the check is keyed strictly on the integration job
// (GITHUB_JOB=="integration"), so local `go test ./...` runs without
// RABBITMQ_TEST_URL continue to skip the gated test quietly, as before.
func TestIntegrationCanary_RABBITMQ_TEST_URL(t *testing.T) {
	if os.Getenv("GITHUB_JOB") == "integration" && os.Getenv("RABBITMQ_TEST_URL") == "" {
		t.Fatal("RABBITMQ_TEST_URL must be set in CI: internal/output/rabbitmq's gated round-trip test would silently skip, defeating the purpose of running it in CI")
	}
}

// TestRabbitMQIntegration_RoundTrip delivers a CDC event through a real
// RabbitMQSinkConsumer against a live RabbitMQ broker and consumes it back
// off the queue, asserting the on-the-wire delivery contract:
//
//   - DLV-04 (idempotency header): the delivered message carries the
//     "Kaptanto-Idempotency-Key" AMQP header matching entry.Event.IdempotencyKey.
//   - DLV-02 (routing by CDC key): the message's routing key is the one
//     rendered from the CDC event via the sink's routing-key-template, and it
//     is what routes the message to the destination queue.
//   - CHK-01 (durability / router-cursor contract): FlushBatch — the call the
//     router waits on before advancing its cursor — does not return until the
//     broker has sent a publisher-confirm ack for the message.
//
// This test previously did not exist: output/rabbitmq was excluded from the
// coverage gate on the (until now, false) premise that "integration covers
// it" — no CI job provisioned a broker of any kind.
func TestRabbitMQIntegration_RoundTrip(t *testing.T) {
	url := rabbitMQTestURL(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Declare a queue directly (bypassing the sink under test) so the
	// default exchange ("") can route to it by routing key == queue name.
	// This is standard RabbitMQ behaviour and lets the test assert delivery
	// without also having to stand up a custom exchange/binding.
	setupConn, err := amqp.Dial(url)
	require.NoError(t, err, "dial RabbitMQ for test setup")
	defer func() { _ = setupConn.Close() }()

	setupCh, err := setupConn.Channel()
	require.NoError(t, err, "open setup channel")
	defer func() { _ = setupCh.Close() }()

	queueName := fmt.Sprintf("kaptanto_test_q_%d", time.Now().UnixNano())
	_, err = setupCh.QueueDeclare(queueName, false /* durable */, true, /* autoDelete */
		false /* exclusive */, false /* noWait */, nil)
	require.NoError(t, err, "declare test queue")
	t.Cleanup(func() {
		_, _ = setupCh.QueueDelete(queueName, false, false, false)
	})

	msgs, err := setupCh.Consume(queueName, "", false /* autoAck */, false, false, false, nil)
	require.NoError(t, err, "start consuming from test queue")

	cfg := config.RabbitMQSinkConfig{
		URL:                url,
		Exchange:           "", // default exchange: routing key == queue name
		RoutingKeyTemplate: "{{.Table}}",
	}
	consumer, err := rabbitmqsink.NewRabbitMQSinkConsumer("rabbitmq-it", cfg)
	require.NoError(t, err, "NewRabbitMQSinkConsumer")
	defer consumer.Close()

	require.NoError(t, consumer.Ping(), "consumer should report a live connection immediately after construction")

	const idempotencyKey = "src:public.orders:1:insert:1"
	entry := eventlog.LogEntry{
		PartitionID: 7,
		Event: &event.ChangeEvent{
			IdempotencyKey: idempotencyKey,
			Table:          queueName, // routing-key-template renders this as the routing key
			Operation:      event.OpInsert,
		},
	}

	require.NoError(t, consumer.Deliver(ctx, entry), "Deliver")

	// CHK-01 / router-cursor contract: FlushBatch must block until the
	// broker's publisher-confirm ack returns. Only after FlushBatch returns
	// nil would the real router be allowed to advance its cursor.
	require.NoError(t, consumer.FlushBatch(ctx, entry.PartitionID), "FlushBatch (waits for publisher confirm)")

	select {
	case d, ok := <-msgs:
		require.True(t, ok, "delivery channel closed before a message arrived")
		require.NoError(t, d.Ack(false))

		// DLV-02: the routing key on the wire is the one derived from the
		// CDC event (here, Table), and it is what routed the message here.
		require.Equal(t, queueName, d.RoutingKey,
			"routing key must be derived from the CDC event per DLV-02")

		// DLV-04: every publish carries the idempotency key header for
		// downstream dedup.
		hdr, ok := d.Headers["Kaptanto-Idempotency-Key"]
		require.True(t, ok, "message must carry Kaptanto-Idempotency-Key header (DLV-04)")
		gotKey, ok := hdr.(string)
		require.True(t, ok, "idempotency header must decode as a string, got %T", hdr)
		require.Equal(t, idempotencyKey, gotKey)
	case <-ctx.Done():
		t.Fatal("timed out waiting for the published message to arrive on the queue")
	}
}
