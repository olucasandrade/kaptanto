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
//     at deliver time (DLV-02).
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

// NATSSinkConsumer is a router.Consumer that publishes events to NATS JetStream.
// It is safe for concurrent calls across different message group keys (RTR-04):
// the NATS client serialises concurrent publishes internally.
//
// Use NewNATSSinkConsumer to construct — do not create directly.
type NATSSinkConsumer struct {
	id       string
	nc       *natsgo.Conn
	js       jetstream.JetStream
	subjectT *template.Template
	m        *observability.KaptantoMetrics
}

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

// Deliver publishes entry.Event to NATS JetStream synchronously (CHK-01).
//
// It blocks until js.PublishMsg returns a PubAck. The cursor in the router
// is NOT advanced until this function returns nil, preserving at-least-once
// delivery semantics.
//
// On error Deliver returns a non-nil error immediately. The RetryScheduler
// is responsible for rescheduling; Deliver never retries internally (DLV-03).
func (c *NATSSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Derive subject from template.
	var buf bytes.Buffer
	if err := c.subjectT.Execute(&buf, entry.Event); err != nil {
		return fmt.Errorf("nats sink: subject template execution: %w", err)
	}
	subject := buf.String()

	// 2. Validate the derived subject using the same rules as the nats.go client.
	// The client library's badSubject() is unexported, so we replicate the logic:
	// - no whitespace characters
	// - no empty tokens after splitting on "."
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

	// 5. Publish synchronously — blocks until PubAck is returned (CHK-01).
	start := time.Now()
	_, pubErr := c.js.PublishMsg(ctx, msg)

	// 6. Record latency regardless of success/failure.
	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("nats").Observe(time.Since(start).Seconds())
	}

	if pubErr != nil {
		if c.m != nil {
			c.m.QueuePublishErrors.WithLabelValues("nats").Inc()
		}
		return fmt.Errorf("nats sink: publish to subject %q: %w", subject, pubErr)
	}

	if c.m != nil {
		c.m.QueuePublishTotal.WithLabelValues("nats").Inc()
	}
	return nil
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
