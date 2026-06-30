// Package natssink provides NATSSinkConsumer, a router.Consumer implementation
// that publishes CDC events to a NATS JetStream server.
//
// Key design decisions:
//   - NATSSinkConsumer connects to a user-configured external NATS URL, never
//     reusing the embedded eventlog connection.
//   - Deliver blocks until js.PublishMsg returns a PubAck, preserving CHK-01:
//     the router's cursor does not advance until the broker has confirmed the write.
//   - Every published message includes a "Kaptanto-Idempotency-Key" header set
//     to entry.Event.IdempotencyKey (DLV-04).
//   - The NATS subject is derived by executing a Go template against the ChangeEvent
//     at deliver time (DLV-02 subject routing); per-key delivery ordering is an
//     RTR-04 router guarantee, not a NATS JetStream feature.
//   - On publish failure Deliver returns a non-nil error; retry is the RetryScheduler's
//     responsibility — NATSSinkConsumer never retries internally (DLV-03).
//   - If StreamName is set in config, NewNATSSinkConsumer validates the stream exists
//     and returns a clear error immediately if it does not (fail-fast).
package natssink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertion: NATSSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*NATSSinkConsumer)(nil)

// pendingNATSMessage holds a pre-encoded NATS message ready for async publish.
type pendingNATSMessage struct {
	msg *natsgo.Msg
}

// NATSSinkConsumer is a router.Consumer that publishes events to NATS JetStream.
// It is safe for concurrent calls across different message group keys (RTR-04):
// the NATS client serialises concurrent publishes internally.
//
// When used with the Router's BatchFlusher interface, Deliver enqueues messages
// into a per-consumer buffer and FlushBatch publishes them all asynchronously,
// then waits for PubAck futures concurrently. CHK-01 is preserved: the router
// only advances the cursor after FlushBatch returns nil.
//
// Use NewNATSSinkConsumer to construct — do not create directly.
type NATSSinkConsumer struct {
	id       string
	nc       *natsgo.Conn
	js       jetstream.JetStream
	subjectT *template.Template
	mu       sync.Mutex
	pending  map[uint32][]pendingNATSMessage
	m        *observability.KaptantoMetrics
}

// Compile-time assertion: NATSSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*NATSSinkConsumer)(nil)

// NewNATSSinkConsumer creates a NATSSinkConsumer connected to cfg.URL.
//
// It returns a non-nil error when:
//   - cfg.SubjectTemplate is not a valid Go template
//   - cfg.URL is unreachable or the NATS connection cannot be established
//   - cfg.StreamName is non-empty and the stream does not exist on the server
//
// The caller is responsible for calling Close() when done.
func NewNATSSinkConsumer(id string, cfg config.NATSSinkConfig) (*NATSSinkConsumer, error) {
	// 1. Parse the subject template early so template errors are caught at startup.
	tmpl, err := template.New("subject").Parse(cfg.SubjectTemplate)
	if err != nil {
		return nil, fmt.Errorf("nats sink: subject template parse error: %w", err)
	}

	// 2. Build NATS connection options.
	opts := []natsgo.Option{
		natsgo.Name("kaptanto-sink"),
		natsgo.MaxReconnects(-1), // reconnect indefinitely
		natsgo.ReconnectWait(2 * time.Second),
		natsgo.ReconnectJitter(500*time.Millisecond, 2*time.Second),
		// Do not retry initial connection — fail fast if the URL is wrong.
		natsgo.Timeout(5 * time.Second),
	}

	if cfg.TLS.CAFile != "" {
		opts = append(opts, natsgo.RootCAs(cfg.TLS.CAFile))
	}

	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		opts = append(opts, natsgo.ClientCert(cfg.TLS.CertFile, cfg.TLS.KeyFile))
	}

	// 3. Connect to the user-configured external NATS server.
	nc, err := natsgo.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats sink: connect to %q: %w", cfg.URL, err)
	}

	// 4. Create JetStream context.
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats sink: jetstream context: %w", err)
	}

	// 5. Validate stream existence if StreamName is configured (fail-fast).
	if cfg.StreamName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := js.Stream(ctx, cfg.StreamName); err != nil {
			nc.Close()
			return nil, fmt.Errorf(
				"nats sink: stream %q does not exist — create it before starting kaptanto: %w",
				cfg.StreamName, err,
			)
		}
	}

	return &NATSSinkConsumer{
		id:       id,
		nc:       nc,
		js:       js,
		subjectT: tmpl,
		pending:  make(map[uint32][]pendingNATSMessage),
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
// It is the id argument passed to NewNATSSinkConsumer.
func (c *NATSSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *NATSSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver enqueues entry.Event into the consumer's pending buffer for batch
// publishing. No network I/O happens here; the actual publish is performed by
// FlushBatch, which is called by the Router once per ReadPartition batch.
//
// This amortises per-event PubAck round-trips: FlushBatch issues all publishes
// asynchronously then awaits PubAck futures concurrently. CHK-01 is preserved:
// the router only advances the cursor after FlushBatch returns nil.
//
// On encoding error Deliver returns a non-nil error immediately; the
// RetryScheduler will block the key (DLV-03).
func (c *NATSSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Derive subject from template.
	var buf bytes.Buffer
	if err := c.subjectT.Execute(&buf, entry.Event); err != nil {
		return fmt.Errorf("nats sink: subject template execution: %w", err)
	}
	subject := buf.String()

	// 2. Validate the derived subject.
	if isInvalidNATSSubject(subject) {
		return fmt.Errorf("nats sink: invalid subject %q derived from template", subject)
	}

	// 3. Marshal the event to JSON.
	data, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("nats sink: marshal event: %w", err)
	}

	// 4. Build message with idempotency header (DLV-04).
	msg := &natsgo.Msg{
		Subject: subject,
		Data:    data,
		Header: natsgo.Header{
			"Kaptanto-Idempotency-Key": []string{entry.Event.IdempotencyKey},
		},
	}

	// 5. Append to pending buffer — FlushBatch performs the actual network call.
	c.mu.Lock()
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingNATSMessage{msg: msg})
	c.mu.Unlock()
	return nil
}

// FlushBatch publishes all buffered messages asynchronously via PublishMsgAsync,
// then collects all PubAck futures. This amortises per-event round-trip latency
// by pipelining publishes before waiting for acks.
//
// CHK-01 is preserved: the router only advances the cursor after FlushBatch
// returns nil for the entire pending set.
func (c *NATSSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.mu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	c.mu.Unlock()

	start := time.Now()

	// Issue all publishes asynchronously and collect PubAck futures.
	futures := make([]jetstream.PubAckFuture, 0, len(batch))
	for _, pm := range batch {
		fut, err := c.js.PublishMsgAsync(pm.msg)
		if err != nil {
			// Synchronous error (e.g. connection closed) — bail early.
			if c.m != nil {
				c.m.QueuePublishErrors.WithLabelValues("nats").Add(float64(len(batch)))
				c.m.QueuePublishLatency.WithLabelValues("nats").Observe(time.Since(start).Seconds())
			}
			return fmt.Errorf("nats sink: publish async to subject %q: %w", pm.msg.Subject, err)
		}
		futures = append(futures, fut)
	}

	// Wait for all acks.
	var firstErr error
	successCount := 0
	for _, fut := range futures {
		select {
		case <-ctx.Done():
			if c.m != nil {
				c.m.QueuePublishLatency.WithLabelValues("nats").Observe(time.Since(start).Seconds())
			}
			return ctx.Err()
		case <-fut.Ok():
			successCount++
		case err := <-fut.Err():
			if firstErr == nil {
				firstErr = fmt.Errorf("nats sink: pubAck error: %w", err)
			}
		}
	}

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("nats").Observe(time.Since(start).Seconds())
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("nats").Add(float64(successCount))
		}
		if firstErr != nil {
			c.m.QueuePublishErrors.WithLabelValues("nats").Add(float64(len(batch) - successCount))
		}
	}
	return firstErr
}

// Ping returns nil when the NATS connection is active and connected, or a
// non-nil error when disconnected or closed. This satisfies the HealthProbe
// Check func() error contract (OBS-02 groundwork).
func (c *NATSSinkConsumer) Ping() error {
	if !c.nc.IsConnected() {
		return fmt.Errorf("nats sink: not connected (status: %v)", c.nc.Status())
	}
	return nil
}

// Close drains and shuts down the underlying NATS connection.
// It is safe to call Close multiple times.
func (c *NATSSinkConsumer) Close() {
	c.nc.Close()
}

// isInvalidNATSSubject returns true if subject contains whitespace or empty tokens,
// mirroring the unexported badSubject function in github.com/nats-io/nats.go.
// NATS subjects must be non-empty, have no whitespace, and have no empty dot-separated tokens.
func isInvalidNATSSubject(subject string) bool {
	if subject == "" {
		return true
	}
	if strings.ContainsAny(subject, " \t\r\n") {
		return true
	}
	for _, token := range strings.Split(subject, ".") {
		if len(token) == 0 {
			return true
		}
	}
	return false
}
