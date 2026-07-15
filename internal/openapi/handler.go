package openapi

import (
	"crypto/sha256"
	"fmt"
	"net/http"
)

// Handler serves the pre-generated OpenAPI spec JSON with ETag caching.
// The spec bytes and ETag are computed once at construction time.
type Handler struct {
	body []byte
	etag string
}

// NewHandler creates a Handler that serves the given spec bytes.
// The ETag is derived from a SHA-256 hash of the content.
func NewHandler(specBytes []byte) *Handler {
	h := sha256.Sum256(specBytes)
	return &Handler{
		body: specBytes,
		etag: fmt.Sprintf(`"%x"`, h),
	}
}

// ServeHTTP writes the spec JSON with appropriate headers.
// Supports If-None-Match for conditional requests (304 Not Modified).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", h.etag)
	w.Header().Set("Cache-Control", "no-cache")

	if match := r.Header.Get("If-None-Match"); match == h.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.body)
}

// ETag returns the handler's current ETag value (for testing).
func (h *Handler) ETag() string {
	return h.etag
}
