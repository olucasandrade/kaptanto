package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildOutputServer_SSERequiresAuthToken asserts that buildOutputServer
// returns an error when output=sse, no auth-token is set, and --insecure is false.
func TestBuildOutputServer_SSERequiresAuthToken(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "sse"
	cfg.Insecure = false
	cfg.AuthToken = "" // no token

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-token")
}

// TestBuildOutputServer_GRPCRequiresAuthToken asserts that buildOutputServer
// returns an error when output=grpc, no auth-token is set, and --insecure is false.
func TestBuildOutputServer_GRPCRequiresAuthToken(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "grpc"
	cfg.Insecure = false
	cfg.AuthToken = "" // no token

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-token")
}

// TestBuildOutputServer_SSEInsecureAllowed asserts that output=sse with no
// auth-token succeeds when --insecure is explicitly set.
func TestBuildOutputServer_SSEInsecureAllowed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "sse"
	cfg.Insecure = true
	cfg.AuthToken = ""

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	fn, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, fn)
}

// TestBuildOutputServer_SSEWithAuth asserts that the real auth.Middleware
// rejects unauthenticated requests and admits valid bearer tokens.
func TestBuildOutputServer_SSEWithAuth(t *testing.T) {
	const token = "supersecret"

	eventsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	protected := auth.Middleware(token, eventsHandler)

	// Request without token → 401.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	protected.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code, "missing token must yield 401")

	// Request with correct token → 200.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/events", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	protected.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code, "valid token must yield 200")

	// Request with lowercase bearer scheme → 200 (case-insensitive).
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/events", nil)
	req3.Header.Set("Authorization", "bearer "+token)
	protected.ServeHTTP(rr3, req3)
	assert.Equal(t, http.StatusOK, rr3.Code, "lowercase bearer scheme must yield 200")
}
