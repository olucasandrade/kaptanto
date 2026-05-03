package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestQueuePublishMetrics tests the three new queue sink metric vectors.
func TestQueuePublishMetrics(t *testing.T) {
	t.Run("NewKaptantoMetrics called twice does not panic", func(t *testing.T) {
		m1 := NewKaptantoMetrics()
		m2 := NewKaptantoMetrics()
		if m1 == nil || m2 == nil {
			t.Fatal("expected non-nil KaptantoMetrics from both calls")
		}
	})

	t.Run("QueuePublishTotal is non-nil", func(t *testing.T) {
		m := NewKaptantoMetrics()
		if m.QueuePublishTotal == nil {
			t.Fatal("expected QueuePublishTotal to be non-nil")
		}
	})

	t.Run("QueuePublishErrors is non-nil", func(t *testing.T) {
		m := NewKaptantoMetrics()
		if m.QueuePublishErrors == nil {
			t.Fatal("expected QueuePublishErrors to be non-nil")
		}
	})

	t.Run("QueuePublishLatency is non-nil", func(t *testing.T) {
		m := NewKaptantoMetrics()
		if m.QueuePublishLatency == nil {
			t.Fatal("expected QueuePublishLatency to be non-nil")
		}
	})

	t.Run("QueuePublishTotal WithLabelValues nats Inc does not panic", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.QueuePublishTotal.WithLabelValues("nats").Inc()
	})

	t.Run("QueuePublishErrors WithLabelValues nats Inc does not panic", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.QueuePublishErrors.WithLabelValues("nats").Inc()
	})

	t.Run("QueuePublishLatency WithLabelValues nats Observe does not panic", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.QueuePublishLatency.WithLabelValues("nats").Observe(0.001)
	})

	t.Run("queue_publish_total appears in /metrics output after Inc", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.QueuePublishTotal.WithLabelValues("nats").Inc()
		h := m.Handler()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		body, _ := io.ReadAll(rr.Body)
		if !strings.Contains(string(body), "queue_publish_total") {
			t.Fatalf("expected queue_publish_total in body, got:\n%s", body)
		}
	})
}

// TestKaptantoMetrics tests all metric behaviors.
func TestKaptantoMetrics(t *testing.T) {
	t.Run("no double-registration panic when called twice", func(t *testing.T) {
		// Should not panic — each call uses a fresh custom registry
		m1 := NewKaptantoMetrics()
		m2 := NewKaptantoMetrics()
		if m1 == nil || m2 == nil {
			t.Fatal("expected non-nil KaptantoMetrics from both calls")
		}
	})

	t.Run("Handler returns HTTP 200", func(t *testing.T) {
		m := NewKaptantoMetrics()
		h := m.Handler()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("events delivered counter appears in /metrics output", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.EventsDelivered.WithLabelValues("consumer-1", "orders", "insert").Inc()
		h := m.Handler()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		body, _ := io.ReadAll(rr.Body)
		if !strings.Contains(string(body), "kaptanto_events_delivered_total") {
			t.Fatalf("expected kaptanto_events_delivered_total in body, got:\n%s", body)
		}
	})

	t.Run("consumer lag gauge appears in /metrics output after Set", func(t *testing.T) {
		m := NewKaptantoMetrics()
		m.ConsumerLag.WithLabelValues("consumer-1").Set(42)
		h := m.Handler()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		body, _ := io.ReadAll(rr.Body)
		if !strings.Contains(string(body), "kaptanto_consumer_lag_events") {
			t.Fatalf("expected kaptanto_consumer_lag_events in body, got:\n%s", body)
		}
	})
}
