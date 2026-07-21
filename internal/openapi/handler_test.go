package openapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerServeHTTP(t *testing.T) {
	body := []byte(`{"openapi":"3.0.3"}`)
	h := NewHandler(body)

	t.Run("GET returns JSON with ETag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatal("ETag header missing")
		}
		if rec.Body.String() != string(body) {
			t.Errorf("body = %q, want %q", rec.Body.String(), string(body))
		}
	})

	t.Run("ETag is stable", func(t *testing.T) {
		req1 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec1 := httptest.NewRecorder()
		h.ServeHTTP(rec1, req1)

		req2 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req2)

		if rec1.Header().Get("ETag") != rec2.Header().Get("ETag") {
			t.Error("ETag is not stable across requests")
		}
	})

	t.Run("If-None-Match returns 304", func(t *testing.T) {
		etag := h.ETag()
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", rec.Code)
		}
	})

	t.Run("POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})
}

func TestHandlerWithAuthMiddleware(t *testing.T) {
	body := []byte(`{"test": true}`)
	h := NewHandler(body)

	mux := http.NewServeMux()
	mux.Handle("/openapi.json", authMiddleware("secret-token", h))

	t.Run("401 without token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("200 with valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Header.Set("Authorization", "Bearer secret-token")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
}

// authMiddleware is a minimal test-only auth middleware that mimics auth.Middleware.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
