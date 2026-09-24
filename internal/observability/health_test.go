package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

	t.Run("503 JSON body contains probe name without error details", func(t *testing.T) {
		h := NewHealthHandler([]HealthProbe{
			{Name: "badger", Check: func() error { return errors.New("disk full at /var/lib/kaptanto host=db.internal user=repl") }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		bodyBytes, _ := io.ReadAll(rr.Body)
		body := string(bodyBytes)
		var status HealthStatus
		if err := json.Unmarshal(bodyBytes, &status); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if status.Healthy {
			t.Fatal("expected Healthy=false")
		}
		msg, ok := status.Checks["badger"]
		if !ok {
			t.Fatal("expected 'badger' key in checks")
		}
		if msg != "unhealthy" {
			t.Fatalf("expected 'unhealthy', got '%s'", msg)
		}
		for _, leak := range []string{"disk full", "db.internal", "user=repl", "/var/lib"} {
			if strings.Contains(body, leak) {
				t.Fatalf("503 body must not leak probe error detail %q; body=%s", leak, body)
			}
		}
	})

	t.Run("probe failure log names the probe and omits error text", func(t *testing.T) {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		t.Cleanup(func() { slog.SetDefault(prev) })

		const secret = "password=super-secret-dsn"
		h := NewHealthHandler([]HealthProbe{
			{Name: "postgres", Check: func() error { return errors.New("connect failed " + secret) }},
		})
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		logged := buf.String()
		if strings.Contains(logged, secret) || strings.Contains(logged, "connect failed") {
			t.Fatalf("healthz log leaked probe error text: %s", logged)
		}
		if !strings.Contains(logged, "postgres") {
			t.Fatalf("healthz log should name the failing probe: %s", logged)
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
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("/healthz returns 200 ok", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "ok" {
			t.Fatalf("expected body 'ok', got '%s'", body)
		}
	})
}
