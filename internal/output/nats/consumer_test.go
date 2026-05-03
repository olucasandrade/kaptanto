// Package natssink_test provides TDD black-box tests for NATSSinkConsumer.
// All tests use an in-process single-node NATS server with JetStream enabled.
package natssink_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	natssink "github.com/olucasandrade/kaptanto/internal/output/nats"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// startTestJSServer starts an in-process single-node NATS server with JetStream enabled.
// Returns the server URL.
func startTestJSServer(t *testing.T) string {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// createStream creates a JetStream stream on the server at url with the given name and subjects.
func createStream(t *testing.T, serverURL string, streamName string, subjects []string) {
	t.Helper()
	nc, err := natsgo.Connect(serverURL)
	require.NoError(t, err)
	defer nc.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: subjects,
	})
	require.NoError(t, err)
}

// makeTestEntry creates an eventlog.LogEntry with the given table and idempotency key.
func makeTestEntry(table, idempotencyKey string) eventlog.LogEntry {
	return eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event: &event.ChangeEvent{
			ID:             ulid.Make(),
			IdempotencyKey: idempotencyKey,
			Timestamp:      time.Now(),
			Source:         "test-source",
			Operation:      event.OpInsert,
			Schema:         "public",
			Table:          table,
			Key:            json.RawMessage(`{"id": 1}`),
			Before:         nil,
			After:          json.RawMessage(`{"id": 1, "name": "test"}`),
			Metadata:       map[string]any{"lsn": "0/1A2B3C4"},
		},
	}
}

// TestNATSSinkConsumer_Deliver_Success verifies:
// - NewNATSSinkConsumer returns no error with a reachable server and valid template.
// - Deliver returns nil error on success.
// - QueuePublishTotal is incremented by 1.
func TestNATSSinkConsumer_Deliver_Success(t *testing.T) {
	serverURL := startTestJSServer(t)
	createStream(t, serverURL, "test-stream", []string{"cdc.>"})

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
		StreamName:      "test-stream",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-1", cfg)
	require.NoError(t, err)
	require.NotNil(t, consumer)
	defer consumer.Close()

	m := observability.NewKaptantoMetrics()
	consumer.SetMetrics(m)

	entry := makeTestEntry("orders", "test:public.orders:1:insert:0/1")
	ctx := context.Background()

	err = consumer.Deliver(ctx, entry)
	require.NoError(t, err)

	// Verify QueuePublishTotal incremented to 1.
	got := testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("nats"))
	assert.Equal(t, float64(1), got, "QueuePublishTotal must be 1 after successful deliver")
}

// TestNATSSinkConsumer_Deliver_Header verifies:
// - The "Kaptanto-Idempotency-Key" header is set on the published message.
func TestNATSSinkConsumer_Deliver_Header(t *testing.T) {
	serverURL := startTestJSServer(t)
	createStream(t, serverURL, "header-stream", []string{"cdc.>"})

	// Subscribe to the subject BEFORE publishing so we capture the message.
	nc, err := natsgo.Connect(serverURL)
	require.NoError(t, err)
	defer nc.Close()

	received := make(chan *natsgo.Msg, 1)
	sub, err := nc.Subscribe("cdc.orders", func(msg *natsgo.Msg) {
		received <- msg
	})
	require.NoError(t, err)
	defer sub.Unsubscribe()

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
		StreamName:      "header-stream",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-header", cfg)
	require.NoError(t, err)
	defer consumer.Close()

	entry := makeTestEntry("orders", "my-idempotency-key-abc123")
	ctx := context.Background()

	err = consumer.Deliver(ctx, entry)
	require.NoError(t, err)

	select {
	case msg := <-received:
		val := msg.Header.Get("Kaptanto-Idempotency-Key")
		assert.Equal(t, "my-idempotency-key-abc123", val,
			"Kaptanto-Idempotency-Key header must match entry.Event.IdempotencyKey")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for published message")
	}
}

// TestNATSSinkConsumer_Deliver_SubjectTemplate verifies:
// - The subject is derived from the Go template applied against entry.Event.
// - Template "cdc.{{.Table}}" with Table="orders" routes to "cdc.orders".
func TestNATSSinkConsumer_Deliver_SubjectTemplate(t *testing.T) {
	serverURL := startTestJSServer(t)
	createStream(t, serverURL, "tmpl-stream", []string{"cdc.>"})

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
		StreamName:      "tmpl-stream",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-tmpl", cfg)
	require.NoError(t, err)
	defer consumer.Close()

	nc, err := natsgo.Connect(serverURL)
	require.NoError(t, err)
	defer nc.Close()

	received := make(chan string, 1)
	sub, err := nc.Subscribe("cdc.orders", func(msg *natsgo.Msg) {
		received <- msg.Subject
	})
	require.NoError(t, err)
	defer sub.Unsubscribe()

	// Flush ensures the server has registered the subscription before we publish.
	require.NoError(t, nc.Flush())

	entry := makeTestEntry("orders", "tmpl-test:public.orders:1:insert:0/1")
	ctx := context.Background()
	err = consumer.Deliver(ctx, entry)
	require.NoError(t, err)

	select {
	case subj := <-received:
		assert.Equal(t, "cdc.orders", subj, "message must be routed to cdc.orders")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for message on cdc.orders")
	}
}

// TestNATSSinkConsumer_Ping verifies:
// - Ping returns nil when connected to a running server.
// - Ping returns a non-nil error after Close (disconnected).
func TestNATSSinkConsumer_Ping(t *testing.T) {
	serverURL := startTestJSServer(t)

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-ping", cfg)
	require.NoError(t, err)

	// While connected: Ping must return nil.
	require.NoError(t, consumer.Ping(), "Ping must return nil when connected")

	// After Close: Ping must return non-nil.
	consumer.Close()
	require.Error(t, consumer.Ping(), "Ping must return error after Close")
}

// TestNATSSinkConsumer_StreamValidation verifies:
// - If StreamName is set and the stream does not exist, NewNATSSinkConsumer returns
//   a non-nil error that contains the stream name.
func TestNATSSinkConsumer_StreamValidation(t *testing.T) {
	serverURL := startTestJSServer(t)

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
		StreamName:      "nonexistent-stream",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-validation", cfg)
	require.Error(t, err, "NewNATSSinkConsumer must return error when stream does not exist")
	assert.Nil(t, consumer, "consumer must be nil when construction fails")
	assert.Contains(t, err.Error(), "nonexistent-stream",
		"error message must contain the stream name")
}

// TestNATSSinkConsumer_ID verifies:
// - ID() returns the id passed to NewNATSSinkConsumer.
func TestNATSSinkConsumer_ID(t *testing.T) {
	serverURL := startTestJSServer(t)

	cfg := config.NATSSinkConfig{
		URL:             serverURL,
		SubjectTemplate: "cdc.{{.Table}}",
	}

	consumer, err := natssink.NewNATSSinkConsumer("my-sink-id", cfg)
	require.NoError(t, err)
	defer consumer.Close()

	assert.Equal(t, "my-sink-id", consumer.ID())
}

// TestNATSSinkConsumer_InvalidURL verifies:
// - NewNATSSinkConsumer returns a non-nil error when the URL is unreachable.
func TestNATSSinkConsumer_InvalidURL(t *testing.T) {
	cfg := config.NATSSinkConfig{
		URL:             "nats://127.0.0.1:14999", // nothing listening here
		SubjectTemplate: "cdc.{{.Table}}",
	}

	consumer, err := natssink.NewNATSSinkConsumer("sink-unreachable", cfg)
	require.Error(t, err, "NewNATSSinkConsumer must return error when URL is unreachable")
	assert.Nil(t, consumer)
}
