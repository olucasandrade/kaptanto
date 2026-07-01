// Package rabbitmqsink provides RabbitMQSinkConsumer, a router.Consumer
// implementation that publishes CDC events to a RabbitMQ exchange using
// the amqp091-go library.
//
// Key design decisions:
//   - CHK-01 (Durability): Deliver blocks until the broker acknowledges the
//     publish via WaitContext on a DeferredConfirmation. The router cursor is NOT
//     advanced until WaitContext returns, preserving at-least-once delivery.
//   - DLV-03 (No internal retry): On any error Deliver returns immediately.
//     Retry is the RetryScheduler's responsibility.
//   - DLV-04 (Idempotency header): Every publish carries a
//     "Kaptanto-Idempotency-Key" AMQP header set to entry.Event.IdempotencyKey.
//   - RTR-04 (Per-key ordering): The channel pool maps entry.PartitionID % 64
//     to a dedicated channel. AMQP channels are NOT goroutine-safe, so each
//     partition slot gets its own channel, matching the 64-partition EventLog.
//   - CGO-free: amqp091-go is a pure Go client; CGO_ENABLED=0 is safe.
package rabbitmqsink

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// Compile-time assertion: RabbitMQSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*RabbitMQSinkConsumer)(nil)

// DeferredConfirmAPI wraps *amqp.DeferredConfirmation.WaitContext to allow
// test injection without a live AMQP broker. Exported so that test packages
// can implement it in fakes.
type DeferredConfirmAPI interface {
	// WaitContext blocks until the broker sends an ack or nack, or ctx is done.
	// Returns (true, nil) on ack, (false, nil) on nack, (false, err) on ctx error.
	WaitContext(ctx context.Context) (bool, error)
}

// AMQPChannelAPI is the interface subset of *amqp.Channel used by Deliver.
// Extracted for test injection — all unit tests pass a fakeAMQPChannel.
// Exported so that test packages can implement it in fakes.
type AMQPChannelAPI interface {
	// PublishWithDeferredConfirmWithContext publishes to exchange/key with publisher
	// confirms enabled. Returns a DeferredConfirmAPI to wait for the broker ack.
	PublishWithDeferredConfirmWithContext(
		ctx context.Context,
		exchange, key string,
		mandatory, immediate bool,
		msg amqp.Publishing,
	) (DeferredConfirmAPI, error)
}

// realDeferredConfirm adapts *amqp.DeferredConfirmation to DeferredConfirmAPI.
type realDeferredConfirm struct {
	dc *amqp.DeferredConfirmation
}

func (r *realDeferredConfirm) WaitContext(ctx context.Context) (bool, error) {
	return r.dc.WaitContext(ctx)
}

// realChannel adapts *amqp.Channel to AMQPChannelAPI.
type realChannel struct {
	ch *amqp.Channel
}

func (r *realChannel) PublishWithDeferredConfirmWithContext(
	ctx context.Context,
	exchange, key string,
	mandatory, immediate bool,
	msg amqp.Publishing,
) (DeferredConfirmAPI, error) {
	dc, err := r.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, immediate, msg)
	if err != nil {
		return nil, err
	}
	return &realDeferredConfirm{dc: dc}, nil
}

// pendingRabbitMQMessage holds a published DeferredConfirmation waiting for broker ack.
type pendingRabbitMQMessage struct {
	dc          DeferredConfirmAPI
	exchange    string
	routingKey  string
}

// RabbitMQSinkConsumer is a router.Consumer that publishes CDC events to a
// RabbitMQ exchange. It maintains a pool of 64 channels — one per EventLog
// partition — ensuring per-channel ordering (AMQP channels are not goroutine-safe).
//
// When used with the Router's BatchFlusher interface, Deliver calls
// PublishWithDeferredConfirmWithContext and stores the DeferredConfirmation;
// FlushBatch waits for all confirms concurrently. This amortises per-event
// WaitContext round-trips. CHK-01 is preserved: the router only advances the
// cursor after FlushBatch returns nil.
//
// A background reconnect goroutine watches for connection drops and re-dials
// with exponential backoff (1s → 30s + jitter).
//
// Use NewRabbitMQSinkConsumer to construct — do not create directly.
type RabbitMQSinkConsumer struct {
	id              string
	cfg             config.RabbitMQSinkConfig
	conn            *amqp.Connection
	channels        [64]AMQPChannelAPI
	mu              sync.RWMutex
	pendingMu       sync.Mutex
	pending         map[uint32][]pendingRabbitMQMessage
	routingKeyT     *template.Template
	cancelReconnect context.CancelFunc
	m               *observability.KaptantoMetrics
}

// Compile-time assertion: RabbitMQSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*RabbitMQSinkConsumer)(nil)

// NewRabbitMQSinkConsumer creates a RabbitMQSinkConsumer connected to cfg.URL.
//
// It returns a non-nil error when:
//   - cfg.RoutingKeyTemplate is not a valid Go template
//   - TLS certificate files are specified but cannot be read or parsed
//   - The AMQP dial fails
//   - Any of the 64 publisher-confirm channels cannot be opened
//
// The caller is responsible for calling Close() when done.
func NewRabbitMQSinkConsumer(id string, cfg config.RabbitMQSinkConfig) (*RabbitMQSinkConsumer, error) {
	// 1. Parse routing key template early — catches config errors at startup.
	tmpl, err := template.New("routing-key").Parse(cfg.RoutingKeyTemplate)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq sink: routing-key-template parse error: %w", err)
	}

	// 2. Build TLS config if a CA file is provided.
	var tlsCfg *tls.Config
	if cfg.TLS.CAFile != "" {
		tlsCfg, err = buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
	}

	// 3. Dial the broker.
	conn, channels, err := dialAndOpenChannels(cfg.URL, tlsCfg)
	if err != nil {
		return nil, err
	}

	// 4. Set up reconnect context.
	ctx, cancel := context.WithCancel(context.Background())

	c := &RabbitMQSinkConsumer{
		id:              id,
		cfg:             cfg,
		conn:            conn,
		channels:        channels,
		routingKeyT:     tmpl,
		cancelReconnect: cancel,
		pending:         make(map[uint32][]pendingRabbitMQMessage),
	}

	// 5. Start reconnect goroutine.
	go c.reconnectLoop(ctx, cfg.URL, tlsCfg)

	return c, nil
}

// NewConsumerWithChannels is an internal constructor for unit tests. It accepts
// a pre-built [64]AMQPChannelAPI array and skips dialing and the reconnect
// goroutine. conn is left nil; Ping will return an error in test consumers.
func NewConsumerWithChannels(id string, cfg config.RabbitMQSinkConfig, channels [64]AMQPChannelAPI) (*RabbitMQSinkConsumer, error) {
	tmpl, err := template.New("routing-key").Parse(cfg.RoutingKeyTemplate)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq sink: routing-key-template parse error: %w", err)
	}
	return &RabbitMQSinkConsumer{
		id:              id,
		cfg:             cfg,
		channels:        channels,
		routingKeyT:     tmpl,
		cancelReconnect: func() {}, // no-op: no reconnect goroutine in tests
		pending:         make(map[uint32][]pendingRabbitMQMessage),
	}, nil
}

// ID returns the stable, unique identifier for this consumer instance.
func (c *RabbitMQSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, and QueuePublishLatency.
// Call after construction, before Deliver.
func (c *RabbitMQSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver publishes entry.Event to the RabbitMQ exchange and stores the
// DeferredConfirmation for batch ack collection via FlushBatch.
//
// It selects channels[entry.PartitionID % 64], derives the routing key from
// the template, marshals the event to JSON, and calls
// PublishWithDeferredConfirmWithContext. The DeferredConfirmation is stored
// in the pending buffer; WaitContext is called in FlushBatch, not here.
//
// This amortises per-event WaitContext latency: all confirms in a batch are
// awaited concurrently in FlushBatch. CHK-01 is preserved: the router only
// advances the cursor after FlushBatch returns nil.
//
// On any publish error Deliver returns immediately — retry is the
// RetryScheduler's responsibility (DLV-03).
func (c *RabbitMQSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// 1. Select channel for this partition (read lock — channels may be swapped
	//    by reconnectLoop under write lock).
	c.mu.RLock()
	ch := c.channels[entry.PartitionID%64]
	c.mu.RUnlock()

	// 2. Derive routing key from template.
	var buf bytes.Buffer
	if err := c.routingKeyT.Execute(&buf, entry.Event); err != nil {
		return fmt.Errorf("rabbitmq sink: routing key template execution: %w", err)
	}
	routingKey := strings.TrimSpace(buf.String())
	if routingKey == "" {
		return fmt.Errorf("rabbitmq sink: routing key template rendered to empty string — check routing-key-template config")
	}

	// 3. Obtain the JSON payload for the message body.
	// Use raw stored bytes when available (pass-through fast path) to avoid
	// the json.Marshal round-trip (fix-plan: raw-bytes-passthrough).
	var data []byte
	if len(entry.Raw) > 0 {
		data = entry.Raw
	} else {
		var err error
		data, err = json.Marshal(entry.Event)
		if err != nil {
			return fmt.Errorf("rabbitmq sink: marshal event: %w", err)
		}
	}

	// 4. Publish with publisher confirms — do NOT wait for ack here.
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, c.cfg.Exchange, routingKey,
		false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         data,
			Headers: amqp.Table{
				"Kaptanto-Idempotency-Key": entry.Event.IdempotencyKey,
			},
		},
	)
	if err != nil {
		if c.m != nil {
			c.m.QueuePublishErrors.WithLabelValues("rabbitmq").Inc()
		}
		return fmt.Errorf("rabbitmq sink: publish to exchange %q routing-key %q: %w",
			c.cfg.Exchange, routingKey, err)
	}

	// 5. Store deferred confirmation for batch ack collection in FlushBatch.
	c.pendingMu.Lock()
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingRabbitMQMessage{
		dc:         dc,
		exchange:   c.cfg.Exchange,
		routingKey: routingKey,
	})
	c.pendingMu.Unlock()
	return nil
}

// FlushBatch awaits all buffered DeferredConfirmations via WaitContext. This
// amortises per-event WaitContext round-trips by waiting for all confirms in a
// batch concurrently.
//
// CHK-01 is preserved: the router only advances the cursor after FlushBatch
// returns nil for the entire pending set.
func (c *RabbitMQSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.pendingMu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.pendingMu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	c.pendingMu.Unlock()

	start := time.Now()
	var firstErr error
	successCount := 0

	for _, pm := range batch {
		acked, waitErr := pm.dc.WaitContext(ctx)
		if waitErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("rabbitmq sink: wait confirm for exchange %q routing-key %q: %w",
					pm.exchange, pm.routingKey, waitErr)
			}
		} else if !acked {
			if firstErr == nil {
				firstErr = fmt.Errorf("rabbitmq sink: broker nacked message on exchange %q routing-key %q",
					pm.exchange, pm.routingKey)
			}
		} else {
			successCount++
		}
	}

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("rabbitmq").Observe(time.Since(start).Seconds())
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("rabbitmq").Add(float64(successCount))
		}
		if firstErr != nil {
			c.m.QueuePublishErrors.WithLabelValues("rabbitmq").Add(float64(len(batch) - successCount))
		}
	}
	return firstErr
}

// Ping returns nil when the AMQP connection is open, or a non-nil error when
// the connection is nil or reports IsClosed() == true.
func (c *RabbitMQSinkConsumer) Ping() error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return fmt.Errorf("rabbitmq sink: connection is closed or not initialized")
	}
	return nil
}

// Close stops the reconnect goroutine, closes all 64 publisher-confirm channels,
// and closes the AMQP connection. It is safe to call Close once.
func (c *RabbitMQSinkConsumer) Close() {
	// Stop the reconnect goroutine first.
	c.cancelReconnect()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Close all 64 channels. We type-assert to *realChannel to reach the underlying
	// *amqp.Channel. If a test-injected fake is present, the assert fails (ok=false)
	// and we skip the close (fakes have no real connection to close).
	for _, ch := range c.channels {
		if rc, ok := ch.(*realChannel); ok {
			_ = rc.ch.Close()
		}
	}

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// reconnectLoop watches for connection drops and re-dials with exponential
// backoff (initial=1s, max=30s, +50% jitter). It runs as a background goroutine
// started by NewRabbitMQSinkConsumer and is stopped when Close() cancels ctx.
func (c *RabbitMQSinkConsumer) reconnectLoop(ctx context.Context, url string, tlsCfg *tls.Config) {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	// Obtain initial close-notification channel.
	notifyClose := conn.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case <-ctx.Done():
			return
		case amqpErr, ok := <-notifyClose:
			// ok=false means the channel was closed — that happens on a graceful
			// Close(). Stop the loop.
			if !ok {
				return
			}
			if amqpErr != nil {
				slog.Warn("rabbitmq sink: connection closed, reconnecting",
					"error", amqpErr.Error())
			}
		}

		// Exponential backoff reconnect loop.
		const maxDelay = 30 * time.Second
		delay := time.Second

		for {
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay + jitter):
			}

			newConn, newChannels, err := dialAndOpenChannels(url, tlsCfg)
			if err != nil {
				slog.Warn("rabbitmq sink: reconnect attempt failed",
					"error", err,
					"next_delay", delay*2)
				if delay < maxDelay {
					delay *= 2
					if delay > maxDelay {
						delay = maxDelay
					}
				}
				continue
			}

			// Swap connection and channels under write lock.
			c.mu.Lock()
			c.conn = newConn
			c.channels = newChannels
			c.mu.Unlock()

			// Start watching the new connection (backoff resets on the next
			// reconnect cycle, which re-initialises delay).
			notifyClose = newConn.NotifyClose(make(chan *amqp.Error, 1))
			slog.Info("rabbitmq sink: reconnected successfully")
			break
		}
	}
}

// dialAndOpenChannels dials the broker and opens 64 publisher-confirm channels.
// Returns the connection and channel array on success, or an error on failure.
func dialAndOpenChannels(url string, tlsCfg *tls.Config) (*amqp.Connection, [64]AMQPChannelAPI, error) {
	var conn *amqp.Connection
	var err error

	if tlsCfg != nil {
		conn, err = amqp.DialTLS(url, tlsCfg)
	} else {
		conn, err = amqp.Dial(url)
	}
	if err != nil {
		return nil, [64]AMQPChannelAPI{}, fmt.Errorf("rabbitmq sink: dial %q: %w", url, err)
	}

	var channels [64]AMQPChannelAPI
	for i := range channels {
		ch, chErr := conn.Channel()
		if chErr != nil {
			_ = conn.Close()
			return nil, [64]AMQPChannelAPI{},
				fmt.Errorf("rabbitmq sink: open channel %d: %w", i, chErr)
		}
		if cfmErr := ch.Confirm(false); cfmErr != nil {
			_ = conn.Close()
			return nil, [64]AMQPChannelAPI{},
				fmt.Errorf("rabbitmq sink: enable publisher confirms on channel %d: %w", i, cfmErr)
		}
		channels[i] = &realChannel{ch: ch}
	}

	return conn, channels, nil
}

// buildTLSConfig constructs a *tls.Config from cfg.
// If CAFile is set, loads the CA certificate pool.
// If CertFile and KeyFile are both set, loads the client key pair for mTLS.
func buildTLSConfig(tlsCfg config.TLSConfig) (*tls.Config, error) {
	cfg := &tls.Config{}

	if tlsCfg.CAFile != "" {
		pem, err := os.ReadFile(tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("rabbitmq sink: read ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("rabbitmq sink: no valid certs in ca-file %q", tlsCfg.CAFile)
		}
		cfg.RootCAs = pool
	}

	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("rabbitmq sink: load client cert: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
