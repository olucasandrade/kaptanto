package mcp

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveConfig_DisabledZeroCost(t *testing.T) {
	cfg, keys, err := ResolveConfig(config.MCPConfig{Enabled: false}, t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, keys)
	assert.False(t, cfg.Enabled)

	s, err := New(Options{Config: config.MCPConfig{Enabled: false}, DataDir: t.TempDir()})
	require.NoError(t, err)
	assert.Nil(t, s)
}

func TestResolveConfig_EnabledWithoutKeys(t *testing.T) {
	_, _, err := ResolveConfig(config.MCPConfig{Enabled: true}, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no api-keys")
}

func TestResolveConfig_StrictEnvRef(t *testing.T) {
	t.Setenv("MCP_TEST_KEY", "super-secret-token")

	_, keys, err := ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{{
			Name: "agent",
			Key:  "${MCP_TEST_KEY}",
		}},
	}, t.TempDir())
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "agent", keys[0].Name)
	assert.Equal(t, "super-secret-token", keys[0].secret)

	_, _, err = ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{{Name: "bad", Key: "literal-secret"}},
	}, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${VAR}")

	_, _, err = ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{{Name: "missing", Key: "${MCP_MISSING_VAR_XYZ}"}},
	}, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unset")
}

func TestLookupKey_ConstantTimeCompare(t *testing.T) {
	keys := []*ResolvedKey{
		{Name: "a", secret: "alpha-token-aaaa"},
		{Name: "b", secret: "beta-token-bbbbb"},
	}
	assert.Equal(t, "a", lookupKey(keys, "alpha-token-aaaa").Name)
	assert.Equal(t, "b", lookupKey(keys, "beta-token-bbbbb").Name)
	assert.Nil(t, lookupKey(keys, "nope"))
	assert.Nil(t, lookupKey(keys, ""))

	// Direct CT compare primitive (same contract as auth.CheckBearer).
	assert.Equal(t, 1, subtle.ConstantTimeCompare([]byte("alpha-token-aaaa"), []byte("alpha-token-aaaa")))
	assert.Equal(t, 0, subtle.ConstantTimeCompare([]byte("alpha-token-aaaa"), []byte("alpha-token-aaab")))
}

func TestServer_AuthUnknown401AndAudit(t *testing.T) {
	t.Setenv("MCP_AUTH_TEST", "correct-bearer-value")

	var auditBuf captureBuf
	auditor := NewAuditorWriter(&auditBuf, slog.New(slog.NewTextHandler(io.Discard, nil)))
	metrics := observability.NewKaptantoMetrics()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_AUTH_TEST}"}},
		},
		DataDir:  t.TempDir(),
		Auditor:  auditor,
		Metrics:  metrics,
		Listener: ln,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	// Wait until listening.
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 10*time.Millisecond)

	url := "http://" + s.Addr().String() + "/"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer wrong-key")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	require.Eventually(t, func() bool {
		return auditBuf.String() != "" && testutil.ToFloat64(metrics.MCPToolCallsTotal.WithLabelValues("auth", OutcomeDenied)) >= 1
	}, time.Second, 10*time.Millisecond)

	assert.Contains(t, auditBuf.String(), `"key":"unknown"`)
	assert.Contains(t, auditBuf.String(), `"outcome":"denied"`)
	assert.NotContains(t, auditBuf.String(), "correct-bearer-value")

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServer_ValidAuthReachesHandler(t *testing.T) {
	t.Setenv("MCP_AUTH_OK", "good-key-value-xyz")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_AUTH_OK}"}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir:  t.TempDir(),
		Listener: ln,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 10*time.Millisecond)

	// Valid bearer: streamable handler answers (not 401). Empty POST may be 400/405 from SDK — anything but 401.
	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr().String()+"/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer good-key-value-xyz")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)

	cancel()
}

func TestNew_ACLCompileErrorAndNilLogger(t *testing.T) {
	t.Setenv("MCP_PORT_TEST", "tok")
	_, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{
				Name:   "bad",
				Key:    "${MCP_PORT_TEST}",
				Tables: []string{"a*b*c"},
			}},
		},
		DataDir: t.TempDir(),
		Logger:  nil,
	})
	require.Error(t, err)
}

func TestRecordToolCall_Metric(t *testing.T) {
	metrics := observability.NewKaptantoMetrics()
	s := &Server{metrics: metrics}
	s.RecordToolCall("k", "list_tables", []string{}, []string{"public.t"}, OutcomeOK, 5*time.Millisecond)
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.MCPToolCallsTotal.WithLabelValues("list_tables", OutcomeOK)))
}

func TestServer_RunWithTLS(t *testing.T) {
	t.Setenv("MCP_TLS_KEY", "tls-secret")
	certPEM, keyPEM := generateTestCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_TLS_KEY}"}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir:  t.TempDir(),
		TLS:      &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Listener: ln,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 5*time.Millisecond)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	}}
	req, err := http.NewRequest(http.MethodPost, "https://"+s.Addr().String()+"/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	cancel()
	<-errCh
}

func TestServer_RunListen(t *testing.T) {
	t.Setenv("MCP_LISTEN", "tok")
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := tmp.Addr().(*net.TCPAddr).Port
	require.NoError(t, tmp.Close())

	s, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			Port:    port,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_LISTEN}"}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestResolveConfig_Defaults(t *testing.T) {
	t.Setenv("MCP_DEF_KEY", "v")
	cfg, _, err := ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_DEF_KEY}"}},
	}, "/data")
	require.NoError(t, err)
	assert.Equal(t, DefaultPort, cfg.Port)
	assert.Equal(t, DefaultMaxSubscriptions, cfg.MaxSubscriptions)
	assert.Equal(t, DefaultRingSize, cfg.RingSize)
	require.NotNil(t, cfg.Audit.Enabled)
	assert.True(t, *cfg.Audit.Enabled)
	assert.Equal(t, "/data/"+DefaultAuditFileName, cfg.Audit.Path)
}

func TestPrincipalFromContext(t *testing.T) {
	assert.Nil(t, PrincipalFromContext(context.Background()))
	k := &ResolvedKey{Name: "x"}
	ctx := context.WithValue(context.Background(), keyPrincipal, k)
	assert.Equal(t, "x", PrincipalFromContext(ctx).Name)
}

func boolPtr(v bool) *bool { return &v }

type captureBuf struct {
	b []byte
}

func (c *captureBuf) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	return len(p), nil
}

func (c *captureBuf) String() string { return string(c.b) }
