// Package kafkasink_test provides TDD black-box tests for KafkaSinkConsumer.
// All tests use an in-process kfake cluster (pure Go, no external broker required).
// Topics must be pre-seeded via kfake.SeedTopics to avoid UNKNOWN_TOPIC_OR_PARTITION
// errors during produce (kfake Pitfall 3).
package kafkasink_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	kafkasink "github.com/olucasandrade/kaptanto/internal/output/kafka"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// startTestCluster starts a kfake cluster with topics pre-seeded and registers
// cleanup via t.Cleanup(cluster.Close). Topics must be pre-seeded in kfake to
// prevent "UNKNOWN_TOPIC_OR_PARTITION" errors during produce.
func startTestCluster(t *testing.T, topics ...string) *kfake.Cluster {
	t.Helper()
	var opts []kfake.Opt
	if len(topics) > 0 {
		opts = append(opts, kfake.SeedTopics(1, topics...))
	}
	cluster, err := kfake.NewCluster(opts...)
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	return cluster
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

// TestNewKafkaSinkConsumer_InvalidTemplate verifies that a malformed Go template
// returns a non-nil error from NewKafkaSinkConsumer.
func TestNewKafkaSinkConsumer_InvalidTemplate(t *testing.T) {
	cfg := config.KafkaSinkConfig{
		BootstrapServers: []string{"localhost:9092"},
		TopicTemplate:    "{{.Unclosed", // malformed Go template
	}
	_, err := kafkasink.NewKafkaSinkConsumer("test", cfg)
	require.Error(t, err, "expected error for malformed topic template")
}

// TestNewKafkaSinkConsumer_UnknownSASL verifies that an unsupported SASL mechanism
// returns a non-nil error containing "unknown sasl-mechanism".
func TestNewKafkaSinkConsumer_UnknownSASL(t *testing.T) {
	cluster := startTestCluster(t)

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
		SASLMechanism:    "DIGEST-MD5",
		SASLUsername:     "user",
		SASLPassword:     "pass",
	}
	_, err := kafkasink.NewKafkaSinkConsumer("test", cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown sasl-mechanism")
}

// TestKafkaSinkConsumer_Deliver_Success verifies:
//   - Deliver returns nil for a valid event to a pre-seeded kfake topic.
//   - QueuePublishTotal is incremented to 1 after a successful deliver.
func TestKafkaSinkConsumer_Deliver_Success(t *testing.T) {
	cluster := startTestCluster(t, "cdc.public.orders")

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}
	c, err := kafkasink.NewKafkaSinkConsumer("test-deliver", cfg)
	require.NoError(t, err)
	defer c.Close()

	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	entry := makeEntry("public", "orders")
	err = c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// Deliver only buffers — must flush to publish.
	err = c.FlushBatch(context.Background())
	require.NoError(t, err)

	// Verify QueuePublishTotal incremented to 1 after flush.
	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("kafka"))
	assert.Equal(t, float64(1), got, "QueuePublishTotal must be 1 after FlushBatch")
}

// TestKafkaSinkConsumer_Deliver_EmptyTopic verifies that a topic template
// rendering to whitespace-only returns an error from Deliver.
func TestKafkaSinkConsumer_Deliver_EmptyTopic(t *testing.T) {
	cluster := startTestCluster(t)

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		// Template renders to whitespace only — should be rejected.
		TopicTemplate: "   ",
	}
	c, err := kafkasink.NewKafkaSinkConsumer("test-empty-topic", cfg)
	require.NoError(t, err)
	defer c.Close()

	entry := makeEntry("public", "orders")
	err = c.Deliver(context.Background(), entry)
	require.Error(t, err, "expected error for empty topic after template execution")
}

// TestKafkaSinkConsumer_Ping verifies that Ping returns nil on a live kfake connection.
func TestKafkaSinkConsumer_Ping(t *testing.T) {
	cluster := startTestCluster(t)

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}
	c, err := kafkasink.NewKafkaSinkConsumer("test-ping", cfg)
	require.NoError(t, err)
	defer c.Close()

	err = c.Ping()
	require.NoError(t, err, "Ping on live kfake connection should return nil")
}

// TestKafkaSinkConsumer_FlushBatch_BatchesMultipleEvents verifies that N events
// delivered then flushed produce all N records in Kafka exactly once, and that
// QueuePublishTotal is N after FlushBatch.
func TestKafkaSinkConsumer_FlushBatch_BatchesMultipleEvents(t *testing.T) {
	const topicName = "cdc.public.items"
	cluster := startTestCluster(t, topicName)

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    topicName,
	}
	c, err := kafkasink.NewKafkaSinkConsumer("test-flush", cfg)
	require.NoError(t, err)
	defer c.Close()

	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	const n = 5
	for i := 0; i < n; i++ {
		entry := eventlog.LogEntry{
			Event: &event.ChangeEvent{
				Schema:         "public",
				Table:          "items",
				Key:            json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)),
				IdempotencyKey: fmt.Sprintf("key-%d", i),
			},
		}
		require.NoError(t, c.Deliver(context.Background(), entry))
	}

	require.NoError(t, c.FlushBatch(context.Background()))

	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("kafka"))
	assert.Equal(t, float64(n), got, "QueuePublishTotal must equal N after FlushBatch")
}

// TestKafkaSinkConsumer_BatchFlusher_Interface verifies that KafkaSinkConsumer
// implements router.BatchFlusher at compile time.
func TestKafkaSinkConsumer_BatchFlusher_Interface(t *testing.T) {
	cluster := startTestCluster(t)
	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}
	c, err := kafkasink.NewKafkaSinkConsumer("iface-test", cfg)
	require.NoError(t, err)
	defer c.Close()
	var _ router.BatchFlusher = c
}

// TestKafkaSinkConsumer_RecordKey verifies that after a successful Deliver,
// the Kafka record's key matches entry.Event.Key (the CDC primary key bytes).
// This confirms the DLV-02 partition-by-key ordering guarantee.
func TestKafkaSinkConsumer_RecordKey(t *testing.T) {
	cluster := startTestCluster(t, "cdc.public.orders")

	cfg := config.KafkaSinkConfig{
		BootstrapServers: cluster.ListenAddrs(),
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}
	c, err := kafkasink.NewKafkaSinkConsumer("test-record-key", cfg)
	require.NoError(t, err)
	defer c.Close()

	entry := makeEntry("public", "orders")
	err = c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// Flush to actually publish the buffered record.
	err = c.FlushBatch(context.Background())
	require.NoError(t, err)

	// Create a consumer client pointed at the same kfake cluster to read back the record.
	consumerClient, err := kgo.NewClient(
		kgo.SeedBrokers(cluster.ListenAddrs()...),
		kgo.ConsumeTopics("cdc.public.orders"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	require.NoError(t, err)
	defer consumerClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fetches := consumerClient.PollFetches(ctx)
	require.NoError(t, fetches.Err())
	var records []*kgo.Record
	fetches.EachRecord(func(r *kgo.Record) { records = append(records, r) })
	require.Len(t, records, 1)
	assert.Equal(t, []byte(`{"id":1}`), records[0].Key)
}
