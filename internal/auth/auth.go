// Package auth provides shared authentication helpers for kaptanto's network
// output servers (SSE and gRPC). It enforces a static bearer token via
// constant-time comparison and exposes HTTP middleware and gRPC interceptors.
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// CheckBearer reports whether the provided token matches expected, using a
// constant-time comparison to prevent timing-based token oracle attacks.
// Both values are compared as UTF-8 byte slices. Returns false when either
// argument is empty.
func CheckBearer(provided, expected string) bool {
	if provided == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// ExtractBearerHTTP returns the bearer token from an HTTP Authorization header
// of the form "Bearer <token>". It returns "" when the header is absent, has
// a different scheme, or has no token part.
func ExtractBearerHTTP(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(hdr, "Bearer ")
}

// ExtractBearerGRPC returns the bearer token from the gRPC incoming metadata
// key "authorization". It handles the common "Bearer <token>" format and also
// accepts a bare token for simple clients. Returns "" when absent.
func ExtractBearerGRPC(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	v := vals[0]
	if strings.HasPrefix(v, "Bearer ") {
		return strings.TrimPrefix(v, "Bearer ")
	}
	return v
}

// Middleware returns an HTTP handler that enforces a static bearer token.
// Requests that supply a valid Authorization: Bearer <token> header are
// forwarded to next; all others receive 401 Unauthorized.
func Middleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CheckBearer(ExtractBearerHTTP(r), token) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UnaryInterceptor returns a gRPC unary server interceptor that enforces a
// static bearer token via the "authorization" metadata key.
func UnaryInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if !CheckBearer(ExtractBearerGRPC(ctx), token) {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a gRPC stream server interceptor that enforces a
// static bearer token via the "authorization" metadata key.
func StreamInterceptor(token string) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !CheckBearer(ExtractBearerGRPC(ss.Context()), token) {
			return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
		}
		return handler(srv, ss)
	}
}
