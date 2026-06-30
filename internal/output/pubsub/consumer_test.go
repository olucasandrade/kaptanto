// Package pubsubsink_test provides TDD black-box tests for PubSubSinkConsumer.
// All tests use an in-process pstest fake server (pure Go, no external broker required).
// Topics MUST be created before publishing — pstest does not auto-create topics.
package pubsubsink_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	pubsubsink "github.com/olucasandrade/kaptanto/internal/output/pubsub"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

const (
	testProject = "test-project"
	testTopic   = "cdc-public-orders"
)

// startFakeServer starts a pstest fake server and returns the server and a connected
// pubsub client. Both are registered for cleanup via t.Cleanup.
func startFakeServer(t *testing.T) (*pstest.Server, *pubsub.Client) {
	t.Helper()
	srv := pstest.NewServer()
	t.Cleanup(func() { srv.Close() })

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client, err := pubsub.NewClient(context.Background(), testProject, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	return srv, client
}

// createTopic creates a Pub/Sub topic on the fake server using the TopicAdminClient.
// The topic must be created before publishing; pstest does not auto-create topics.
func createTopic(t *testing.T, client *pubsub.Client, projectID, topicID string) {
	t.Helper()
	ctx := context.Background()
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/" + projectID + "/topics/" + topicID,
	})
	require.NoError(t, err, "createTopic: failed to create topic %q in project %q", topicID, projectID)
}

// makeEntry creates a minimal LogEntry for tests.
func makeEntry(schema, table string) eventlog.LogEntry {
	return eventlog.LogEntry{
		Event: &event.ChangeEvent{
			Schema:         schema,
			Table:          table,
			Operation:      "insert",
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: "test-idempotency-key-01",
		},
	}
}

// makeConsumerWithFakeServer creates a PubSubSinkConsumer wired to a pstest fake server.
// The fake server connection is injected via option.WithGRPCConn so no real GCP credentials
// are required. The consumer is registered for cleanup via t.Cleanup.
func makeConsumerWithFakeServer(t *testing.T, srv *pstest.Server, projectID, topicID string) *pubsubsink.PubSubSinkConsumer {
	t.Helper()

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	cfg := config.PubSubSinkConfig{
		ProjectID: projectID,
		TopicID:   topicID,
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("test-consumer", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return c
}

// TestNewPubSubSinkConsumer_InvalidTemplate verifies that a malformed Go template
// returns a non-nil error from NewPubSubSinkConsumer.
func TestNewPubSubSinkConsumer_InvalidTemplate(t *testing.T) {
	cfg := config.PubSubSinkConfig{
		ProjectID:     "my-project",
		TopicID:       "my-topic",
		TopicTemplate: "{{.Unclosed",
	}
	_, err := pubsubsink.NewPubSubSinkConsumer("test", cfg)
	require.Error(t, err, "expected error for malformed topic template")
}

// TestPubSubSinkConsumer_Deliver_Success verifies:
//   - Deliver returns nil for a valid event delivered to a pre-created pstest topic.
//   - QueuePublishTotal WithLabelValues("pubsub") == 1.0 after a successful deliver.
func TestPubSubSinkConsumer_Deliver_Success(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)

	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry("public", "orders")
	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// Deliver only buffers — flush to publish and await ack.
	err = c.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("pubsub"))
	assert.Equal(t, float64(1), got, "QueuePublishTotal must be 1 after FlushBatch")
}

// TestPubSubSinkConsumer_Deliver_OrderingKey verifies:
//   - The published message has OrderingKey == string(entry.Event.Key) (DLV-02).
//   - The published message carries the Kaptanto-Idempotency-Key attribute (DLV-04).
func TestPubSubSinkConsumer_Deliver_OrderingKey(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	// Create a subscription before publishing so we can pull the message.
	ctx := context.Background()
	subName := "projects/" + testProject + "/subscriptions/test-sub"
	_, err := client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subName,
		Topic: "projects/" + testProject + "/topics/" + testTopic,
	})
	require.NoError(t, err)

	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)

	entry := eventlog.LogEntry{
		Event: &event.ChangeEvent{
			Schema:         "public",
			Table:          "orders",
			Operation:      "insert",
			Key:            json.RawMessage(`{"id":42}`),
			IdempotencyKey: "idem-ordering-key-01",
		},
	}

	err = c.Deliver(context.Background(), entry)
	require.NoError(t, err)
	err = c.FlushBatch(context.Background(), 0)
	require.NoError(t, err)

	// Pull the published message via the fake server's subscription.
	subscriber := client.Subscriber("test-sub")
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotOrderingKey string
	var gotIdempotencyKey string
	err = subscriber.Receive(ctx2, func(ctx context.Context, msg *pubsub.Message) {
		gotOrderingKey = msg.OrderingKey
		gotIdempotencyKey = msg.Attributes["Kaptanto-Idempotency-Key"]
		msg.Ack()
		cancel() // stop receiving after first message
	})
	// Receive returns context.Canceled when cancel() is called — that's expected.
	if err != nil && err != context.Canceled {
		require.NoError(t, err)
	}

	assert.Equal(t, `{"id":42}`, gotOrderingKey, "OrderingKey must equal string(entry.Event.Key)")
	assert.Equal(t, "idem-ordering-key-01", gotIdempotencyKey, "Kaptanto-Idempotency-Key attribute must equal entry.Event.IdempotencyKey")
}

// TestPubSubSinkConsumer_Ping verifies that Ping() returns nil when the topic exists.
func TestPubSubSinkConsumer_Ping(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)

	err := c.Ping()
	require.NoError(t, err, "Ping should return nil when topic exists on pstest server")
}

// TestPubSubSinkConsumer_Close verifies that Close() does not panic and that a
// second call to Close() is also safe (idempotency).
func TestPubSubSinkConsumer_Close(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	cfg := config.PubSubSinkConfig{
		ProjectID: testProject,
		TopicID:   testTopic,
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("test-close", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)

	assert.NotPanics(t, func() { c.Close() }, "first Close() must not panic")
	assert.NotPanics(t, func() { c.Close() }, "second Close() must not panic")
}

// TestPubSubSinkConsumer_FlushBatch_MetricsError verifies that FlushBatch to a
// non-existent topic returns a non-nil error and increments QueuePublishErrors.
func TestPubSubSinkConsumer_FlushBatch_MetricsError(t *testing.T) {
	srv, _ := startFakeServer(t)
	// Intentionally do NOT create the topic — publish should fail on flush.

	c := makeConsumerWithFakeServer(t, srv, testProject, "non-existent-topic")

	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry("public", "orders")
	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err, "Deliver should not error — it only buffers")

	err = c.FlushBatch(context.Background(), 0)
	require.Error(t, err, "expected error when publishing to non-existent topic")

	got := testutil.ToFloat64(m.QueuePublishErrors.WithLabelValues("pubsub"))
	assert.GreaterOrEqual(t, got, float64(1), "QueuePublishErrors must be >= 1 after publish failure")
}

// TestPubSubSinkConsumer_PerTableRouting verifies that Deliver routes events from
// different tables to different Pub/Sub topics when TopicTemplate is set.
// Two events (public.orders and public.users) must land on separate topics
// (cdc-public-orders and cdc-public-users) as observed via srv.Messages().Topic.
func TestPubSubSinkConsumer_PerTableRouting(t *testing.T) {
	srv, client := startFakeServer(t)
	// Must create both topics before publishing — pstest does not auto-create topics.
	createTopic(t, client, testProject, "cdc-public-orders")
	createTopic(t, client, testProject, "cdc-public-users")

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	cfg := config.PubSubSinkConfig{
		ProjectID:     testProject,
		TopicID:       "cdc-public-orders", // default topic
		TopicTemplate: "cdc-{{.Schema}}-{{.Table}}",
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("routing-test", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer c.Close()

	err = c.Deliver(context.Background(), makeEntry("public", "orders"))
	require.NoError(t, err)
	err = c.Deliver(context.Background(), makeEntry("public", "users"))
	require.NoError(t, err)
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	msgs := srv.Messages()
	var ordersCount, usersCount int
	for _, m := range msgs {
		switch {
		case strings.Contains(m.Topic, "cdc-public-orders"):
			ordersCount++
		case strings.Contains(m.Topic, "cdc-public-users"):
			usersCount++
		}
	}
	assert.Equal(t, 1, ordersCount, "expected 1 message on cdc-public-orders topic")
	assert.Equal(t, 1, usersCount, "expected 1 message on cdc-public-users topic")
}

// TestPubSubSinkConsumer_PoolReusesSamePublisher verifies that delivering two events
// to the same resolved topic uses the same pooled publisher (no crash or duplication).
// Both messages must appear on the resolved topic.
func TestPubSubSinkConsumer_PoolReusesSamePublisher(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, "cdc-public-orders")

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	cfg := config.PubSubSinkConfig{
		ProjectID:     testProject,
		TopicID:       "cdc-public-orders",
		TopicTemplate: "cdc-{{.Schema}}-{{.Table}}",
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("reuse-test", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer c.Close()

	// Two delivers to the same resolved topic — pool must not panic or duplicate.
	err = c.Deliver(context.Background(), makeEntry("public", "orders"))
	require.NoError(t, err)
	err = c.Deliver(context.Background(), makeEntry("public", "orders"))
	require.NoError(t, err)
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	msgs := srv.Messages()
	var count int
	for _, m := range msgs {
		if strings.Contains(m.Topic, "cdc-public-orders") {
			count++
		}
	}
	assert.Equal(t, 2, count, "expected 2 messages on cdc-public-orders topic")
}

// TestPubSubSinkConsumer_CloseDrainsAllPublishers verifies that Close() does not panic
// when the publisher pool contains publishers for multiple topics. It drains all pooled
// publishers before returning.
func TestPubSubSinkConsumer_CloseDrainsAllPublishers(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, "cdc-public-orders")
	createTopic(t, client, testProject, "cdc-public-users")

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	// Do not t.Cleanup conn here — Close() will drain and we close conn after.

	cfg := config.PubSubSinkConfig{
		ProjectID:     testProject,
		TopicID:       "cdc-public-orders",
		TopicTemplate: "cdc-{{.Schema}}-{{.Table}}",
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("close-all-test", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)

	ctx := context.Background()
	err = c.Deliver(ctx, makeEntry("public", "orders"))
	require.NoError(t, err)
	err = c.Deliver(ctx, makeEntry("public", "users"))
	require.NoError(t, err)
	// Flush before close to drain pending messages.
	require.NoError(t, c.FlushBatch(ctx, 0))

	// Close must not panic — it must drain all 2 pooled publishers.
	assert.NotPanics(t, func() { c.Close() })
	assert.NotPanics(t, func() { conn.Close() })
}

// TestPubSubSinkConsumer_Deliver_EmptyTemplateResult verifies that Deliver returns a
// non-nil error containing "empty string" when TopicTemplate evaluates to an empty
// string after TrimSpace. The consumer must not crash or publish to an invalid topic.
func TestPubSubSinkConsumer_Deliver_EmptyTemplateResult(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	// TopicTemplate that always renders to an empty string after TrimSpace.
	cfg := config.PubSubSinkConfig{
		ProjectID:     testProject,
		TopicID:       testTopic,
		TopicTemplate: "{{if false}}something{{end}}",
	}
	c, err := pubsubsink.NewPubSubSinkConsumer("empty-tmpl-test", cfg, option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer c.Close()

	err = c.Deliver(context.Background(), makeEntry("public", "orders"))
	require.Error(t, err, "expected error when template renders to empty string")
	assert.Contains(t, err.Error(), "empty string")
}

// TestPubSubSinkConsumer_Deliver_NoTemplate_Regression verifies that when TopicTemplate
// is empty (the Phase 22 default), Deliver publishes to cfg.TopicID without regression.
func TestPubSubSinkConsumer_Deliver_NoTemplate_Regression(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	// No TopicTemplate — must behave identically to Phase 22.
	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)

	err := c.Deliver(context.Background(), makeEntry("public", "orders"))
	require.NoError(t, err)
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	msgs := srv.Messages()
	var count int
	for _, m := range msgs {
		if strings.Contains(m.Topic, testTopic) {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected 1 message on default topic (no template regression)")
}

// TestPubSubSinkConsumer_FlushBatch_BatchesMultipleEvents verifies that N events
// delivered then flushed produce exactly N messages on the topic.
func TestPubSubSinkConsumer_FlushBatch_BatchesMultipleEvents(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)

	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	const n = 5
	for i := 0; i < n; i++ {
		entry := eventlog.LogEntry{
			Event: &event.ChangeEvent{
				Schema:         "public",
				Table:          "orders",
				Key:            json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)),
				IdempotencyKey: fmt.Sprintf("key-%d", i),
			},
		}
		require.NoError(t, c.Deliver(context.Background(), entry))
	}
	require.NoError(t, c.FlushBatch(context.Background(), 0))

	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("pubsub"))
	assert.Equal(t, float64(n), got, "QueuePublishTotal must equal N after FlushBatch")
}

// TestPubSubSinkConsumer_BatchFlusher_Interface verifies that PubSubSinkConsumer
// implements router.BatchFlusher at compile time.
func TestPubSubSinkConsumer_BatchFlusher_Interface(t *testing.T) {
	srv, client := startFakeServer(t)
	createTopic(t, client, testProject, testTopic)
	c := makeConsumerWithFakeServer(t, srv, testProject, testTopic)
	var _ router.BatchFlusher = c
}
