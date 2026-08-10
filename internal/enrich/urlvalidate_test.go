package enrich

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEnricherURL_BlocksMetadataIP(t *testing.T) {
	t.Parallel()
	policy := newURLPolicy(nil, false)
	_, err := validateEnricherURL("http://169.254.169.254/latest/meta-data/", policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestValidateEnricherURL_BlocksMetadataHostname(t *testing.T) {
	t.Parallel()
	policy := newURLPolicy(nil, false)
	_, err := validateEnricherURL("http://metadata.google.internal/computeMetadata/v1/", policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked metadata host")
}

func TestValidateEnricherURL_AllowsLoopback(t *testing.T) {
	t.Parallel()
	policy := newURLPolicy(nil, false)
	u, err := validateEnricherURL("http://127.0.0.1:8080/enrich", policy)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", u.Hostname())
}

func TestValidateEnricherURL_AllowlistPermitsPrivate(t *testing.T) {
	t.Parallel()
	policy := newURLPolicy([]string{"10.1.2.3"}, false)
	u, err := validateEnricherURL("http://10.1.2.3/enrich", policy)
	require.NoError(t, err)
	assert.Equal(t, "10.1.2.3", u.Hostname())
}

func TestURLPolicy_isBlockedIP(t *testing.T) {
	t.Parallel()
	policy := newURLPolicy(nil, false)
	assert.False(t, policy.isBlockedIP(parseTestIP(t, "127.0.0.1")))
	assert.True(t, policy.isBlockedIP(parseTestIP(t, "10.0.0.1")))
	assert.True(t, policy.isBlockedIP(parseTestIP(t, "169.254.1.1")))
}

func parseTestIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	require.NotNil(t, ip)
	return ip
}

func TestURLPolicy_dialContext_DialsVettedIP(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	policy := newURLPolicy(nil, false)
	var lookupCalls int
	policy.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		lookupCalls++
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}

	conn, err := policy.dialContext(context.Background(), "tcp", net.JoinHostPort("rebind.example", port))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, 1, lookupCalls)
}
