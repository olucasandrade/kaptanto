package cmd

import (
	"net/http"
	"testing"
)

// TestNewHTTPServerTimeouts asserts the shared HTTP server constructor sets a
// non-zero ReadHeaderTimeout and IdleTimeout (Slowloris / idle-connection
// defense) while leaving WriteTimeout at 0 so long-lived SSE streams are not
// terminated mid-flight.
func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(":0", http.NewServeMux())

	if srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want > 0", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %v, want > 0", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 (SSE streams are long-lived)", srv.WriteTimeout)
	}
}
