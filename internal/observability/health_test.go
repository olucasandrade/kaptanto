package observability

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthHandler tests all health endpoint behaviors.
func TestHealthHandler(t *testing.T) {
	t.Run("returns 200 with body ok when no probes registered", func(t *testing.T) {
		h := NewHealthHandler(nil)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		body, _ := io.ReadAll(rr.Body)
		if string(body) != "ok" {
			t.Fatalf("expected body 'ok', got '%s'", body)
		}
	})

	t.Run("returns 200 when all registered probes return healthy", func(t *testing.T) {
		h := NewHealthHandler([]HealthProbe{
			{Name: "db", Check: func() error { return nil }},
			{Name: "cache", Check: func() error { return nil }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("returns 503 with Content-Type application/json when any probe returns unhealthy", func(t *testing.T) {
		h := NewHealthHandler([]HealthProbe{
			{Name: "db", Check: func() error { return errors.New("connection refused") }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rr.Code)
		}
		ct := rr.Header().Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Fatalf("expected Content-Type application/json, got %s", ct)
		}
	})

	t.Run("503 JSON body contains probe name and error message", func(t *testing.T) {
		h := NewHealthHandler([]HealthProbe{
			{Name: "badger", Check: func() error { return errors.New("disk full") }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var status HealthStatus
		if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if status.Healthy {
			t.Fatal("expected Healthy=false")
		}
		if msg, ok := status.Checks["badger"]; !ok {
			t.Fatal("expected 'badger' key in checks")
		} else if msg != "disk full" {
			t.Fatalf("expected 'disk full', got '%s'", msg)
		}
	})

	t.Run("multiple probes — all healthy except one — returns 503 with only failing probe", func(t *testing.T) {
		h := NewHealthHandler([]HealthProbe{
			{Name: "db", Check: func() error { return nil }},
			{Name: "wal", Check: func() error { return errors.New("wal stalled") }},
			{Name: "cache", Check: func() error { return nil }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rr.Code)
		}
		var status HealthStatus
		if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if len(status.Checks) != 1 {
			t.Fatalf("expected 1 failing check, got %d: %v", len(status.Checks), status.Checks)
		}
		if _, ok := status.Checks["wal"]; !ok {
			t.Fatalf("expected 'wal' key in failing checks, got: %v", status.Checks)
		}
	})
}

// TestObservabilityServer is the integration test that verifies both /metrics
// and /healthz are reachable via a real HTTP round-trip using httptest.NewServer.
func TestObservabilityServer(t *testing.T) {
	m := NewKaptantoMetrics()
	health := NewHealthHandler([]HealthProbe{
		{Name: "self", Check: func() error { return nil }},
	})

	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.Handle("/healthz", health)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("/metrics returns 200", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("/healthz returns 200 ok", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "ok" {
			t.Fatalf("expected body 'ok', got '%s'", body)
		}
	})
}
