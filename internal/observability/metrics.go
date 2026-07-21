package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// KaptantoMetrics holds all Prometheus metric vectors for kaptanto.
// Uses a custom registry (not global DefaultRegisterer) to prevent
// duplicate-registration panics in tests.
type KaptantoMetrics struct {
	reg                   *prometheus.Registry
	EventsDelivered       *prometheus.CounterVec   // kaptanto_events_delivered_total{consumer,table,operation}
	ConsumerLag           *prometheus.GaugeVec     // kaptanto_consumer_lag_events{consumer}
	ErrorsTotal           *prometheus.CounterVec   // kaptanto_errors_total{consumer,kind}
	SourceLagBytes        *prometheus.GaugeVec     // kaptanto_source_lag_bytes{source}
	CheckpointFlushes     prometheus.Counter       // kaptanto_checkpoint_flushes_total
	QueuePublishTotal     *prometheus.CounterVec   // queue_publish_total{sink}
	QueuePublishErrors    *prometheus.CounterVec   // queue_publish_errors_total{sink}
	QueuePublishLatency   *prometheus.HistogramVec // queue_publish_latency_seconds{sink}
	DLQEventsTotal        *prometheus.CounterVec   // kaptanto_dlq_events_total{consumer}
	DLQWriteFailuresTotal *prometheus.CounterVec   // kaptanto_dlq_write_failures_total{consumer}
	TransformDroppedTotal *prometheus.CounterVec   // kaptanto_transform_dropped_total{consumer}
	TransformErrorsTotal  *prometheus.CounterVec   // kaptanto_transform_errors_total{consumer}
	ActionEventsMatched   *prometheus.CounterVec   // kaptanto_action_events_matched_total{consumer}
	ActionEventsSkipped   *prometheus.CounterVec   // kaptanto_action_events_skipped_total{consumer}
	MCPToolCallsTotal     *prometheus.CounterVec   // mcp_tool_calls_total{tool,outcome}
}

// NewKaptantoMetrics creates a KaptantoMetrics with a fresh custom Prometheus
// registry. Calling it multiple times in the same process never panics because
// each call allocates its own prometheus.Registry instead of using the global
// DefaultRegisterer.
func NewKaptantoMetrics() *KaptantoMetrics {
	reg := prometheus.NewRegistry()
	m := &KaptantoMetrics{
		reg: reg,
		EventsDelivered: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_events_delivered_total",
			Help: "Total events delivered, labeled by consumer, table, and operation.",
		}, []string{"consumer", "table", "operation"}),
		ConsumerLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kaptanto_consumer_lag_events",
			Help: "Number of events the consumer is behind the Event Log head.",
		}, []string{"consumer"}),
		ErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_errors_total",
			Help: "Total errors, labeled by consumer and kind (deliver, flush, grpc).",
		}, []string{"consumer", "kind"}),
		SourceLagBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kaptanto_source_lag_bytes",
			Help: "WAL lag in bytes between source write LSN and flush LSN.",
		}, []string{"source"}),
		CheckpointFlushes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kaptanto_checkpoint_flushes_total",
			Help: "Total number of consumer cursor flush operations to SQLite.",
		}),
		QueuePublishTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "queue_publish_total",
			Help: "Total events published to queue sinks, labeled by sink type.",
		}, []string{"sink"}),
		QueuePublishErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "queue_publish_errors_total",
			Help: "Total publish errors to queue sinks, labeled by sink type.",
		}, []string{"sink"}),
		QueuePublishLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "queue_publish_latency_seconds",
			Help:    "Publish round-trip latency to queue sinks in seconds, labeled by sink type.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sink"}),
		DLQEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_dlq_events_total",
			Help: "Total events written to the dead-letter queue, labeled by consumer.",
		}, []string{"consumer"}),
		DLQWriteFailuresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_dlq_write_failures_total",
			Help: "Total failed writes to the dead-letter queue, labeled by consumer.",
		}, []string{"consumer"}),
		TransformDroppedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_transform_dropped_total",
			Help: "Total events dropped by transform filters, labeled by consumer.",
		}, []string{"consumer"}),
		TransformErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_transform_errors_total",
			Help: "Total transform evaluation errors, labeled by consumer.",
		}, []string{"consumer"}),
		ActionEventsMatched: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_action_events_matched_total",
			Help: "Total events matched by action routing, labeled by consumer.",
		}, []string{"consumer"}),
		ActionEventsSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kaptanto_action_events_skipped_total",
			Help: "Total events skipped by action routing, labeled by consumer.",
		}, []string{"consumer"}),
		MCPToolCallsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_tool_calls_total",
			Help: "Total MCP tool calls, labeled by tool name and outcome (ok|denied|error).",
		}, []string{"tool", "outcome"}),
	}
	reg.MustRegister(
		m.EventsDelivered,
		m.ConsumerLag,
		m.ErrorsTotal,
		m.SourceLagBytes,
		m.CheckpointFlushes,
		m.QueuePublishTotal,
		m.QueuePublishErrors,
		m.QueuePublishLatency,
		m.DLQEventsTotal,
		m.DLQWriteFailuresTotal,
		m.TransformDroppedTotal,
		m.TransformErrorsTotal,
		m.ActionEventsMatched,
		m.ActionEventsSkipped,
		m.MCPToolCallsTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns an http.Handler that exposes the /metrics endpoint
// using the custom registry. Mount this at /metrics on the observability mux.
func (m *KaptantoMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// Registry exposes the underlying prometheus.Registry for test verification.
func (m *KaptantoMetrics) Registry() *prometheus.Registry {
	return m.reg
}
