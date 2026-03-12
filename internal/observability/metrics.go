package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// KaptantoMetrics holds all Prometheus metric vectors for kaptanto.
// Uses a custom registry (not global DefaultRegisterer) to prevent
// duplicate-registration panics in tests.
type KaptantoMetrics struct {
	reg               *prometheus.Registry
	EventsDelivered   *prometheus.CounterVec // kaptanto_events_delivered_total{consumer,table,operation}
	ConsumerLag       *prometheus.GaugeVec   // kaptanto_consumer_lag_events{consumer}
	ErrorsTotal       *prometheus.CounterVec // kaptanto_errors_total{consumer,kind}
	SourceLagBytes    *prometheus.GaugeVec   // kaptanto_source_lag_bytes{source}
	CheckpointFlushes prometheus.Counter     // kaptanto_checkpoint_flushes_total
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
	}
	reg.MustRegister(
		m.EventsDelivered,
		m.ConsumerLag,
		m.ErrorsTotal,
		m.SourceLagBytes,
		m.CheckpointFlushes,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns an http.Handler that exposes the /metrics endpoint
// using the custom registry. Mount this at /metrics on the observability mux.
func (m *KaptantoMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}
