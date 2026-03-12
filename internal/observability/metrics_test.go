package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
