package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// ─── CheckBearer ─────────────────────────────────────────────────────────────

func TestCheckBearer_Match(t *testing.T) {
	assert.True(t, auth.CheckBearer("secret", "secret"))
}

func TestCheckBearer_Mismatch(t *testing.T) {
	assert.False(t, auth.CheckBearer("wrong", "secret"))
}

func TestCheckBearer_EmptyProvided(t *testing.T) {
	assert.False(t, auth.CheckBearer("", "secret"))
}

func TestCheckBearer_EmptyExpected(t *testing.T) {
	assert.False(t, auth.CheckBearer("token", ""))
}

func TestCheckBearer_BothEmpty(t *testing.T) {
	assert.False(t, auth.CheckBearer("", ""))
}

// ─── ExtractBearerHTTP ───────────────────────────────────────────────────────

func TestExtractBearerHTTP_Valid(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer mytoken")
	assert.Equal(t, "mytoken", auth.ExtractBearerHTTP(r))
}

func TestExtractBearerHTTP_Missing(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "", auth.ExtractBearerHTTP(r))
}

func TestExtractBearerHTTP_WrongScheme(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	assert.Equal(t, "", auth.ExtractBearerHTTP(r))
}

// ─── ExtractBearerGRPC ───────────────────────────────────────────────────────

func TestExtractBearerGRPC_Valid(t *testing.T) {
	md := metadata.Pairs("authorization", "Bearer grpctoken")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	assert.Equal(t, "grpctoken", auth.ExtractBearerGRPC(ctx))
}

func TestExtractBearerGRPC_BareToken(t *testing.T) {
	md := metadata.Pairs("authorization", "baretoken")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	assert.Equal(t, "baretoken", auth.ExtractBearerGRPC(ctx))
}

func TestExtractBearerGRPC_NoMetadata(t *testing.T) {
	assert.Equal(t, "", auth.ExtractBearerGRPC(context.Background()))
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestMiddleware_ValidToken(t *testing.T) {
	h := auth.Middleware("secret", http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_MissingToken(t *testing.T) {
	h := auth.Middleware("secret", http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_WrongToken(t *testing.T) {
	h := auth.Middleware("secret", http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ─── gRPC interceptors (unit-level) ──────────────────────────────────────────

func TestUnaryInterceptor_ValidToken(t *testing.T) {
	interceptor := auth.UnaryInterceptor("secret")
	md := metadata.Pairs("authorization", "Bearer secret")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	called := false
	_, err := interceptor(ctx, nil, nil, func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestUnaryInterceptor_MissingToken(t *testing.T) {
	interceptor := auth.UnaryInterceptor("secret")
	_, err := interceptor(context.Background(), nil, nil, func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unauthenticated")
}

func TestUnaryInterceptor_WrongToken(t *testing.T) {
	interceptor := auth.UnaryInterceptor("secret")
	md := metadata.Pairs("authorization", "Bearer wrong")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, nil, func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unauthenticated")
}
