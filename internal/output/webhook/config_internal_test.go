package webhooksink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

func TestBuildTLSConfig_Empty(t *testing.T) {
	cfg, err := buildTLSConfig(config.TLSConfig{})
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	assert.Nil(t, cfg.RootCAs)
	assert.Empty(t, cfg.Certificates)
}

func TestBuildTLSConfig_CAAndMTLS(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := genSelfSignedPEM(t)
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, certPEM, 0o600))

	cfg, err := buildTLSConfig(config.TLSConfig{CAFile: caPath})
	require.NoError(t, err)
	assert.NotNil(t, cfg.RootCAs)

	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	cfg, err = buildTLSConfig(config.TLSConfig{CAFile: caPath, CertFile: certPath, KeyFile: keyPath})
	require.NoError(t, err)
	assert.Len(t, cfg.Certificates, 1)
}

func TestBuildTLSConfig_Errors(t *testing.T) {
	t.Run("missing ca-file", func(t *testing.T) {
		_, err := buildTLSConfig(config.TLSConfig{CAFile: "/no/such/ca.pem"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read ca-file")
	})
	t.Run("ca-file with no certs", func(t *testing.T) {
		dir := t.TempDir()
		bad := filepath.Join(dir, "bad.pem")
		require.NoError(t, os.WriteFile(bad, []byte("not a pem"), 0o600))
		_, err := buildTLSConfig(config.TLSConfig{CAFile: bad})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no valid certs")
	})
	t.Run("bad client key pair", func(t *testing.T) {
		dir := t.TempDir()
		bad := filepath.Join(dir, "bad.pem")
		require.NoError(t, os.WriteFile(bad, []byte("nope"), 0o600))
		_, err := buildTLSConfig(config.TLSConfig{CertFile: bad, KeyFile: bad})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load client cert")
	})
}

func TestNewWebhookSinkConsumer_TLSFileError(t *testing.T) {
	_, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL: "http://example.com",
		TLS: config.TLSConfig{CAFile: "/no/such/ca.pem"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read ca-file")
}

func TestPing_HTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{URL: srv.URL})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	// httptest TLS uses a self-signed cert; Ping dials with the client's TLS config
	// which trusts system CAs by default — expect error OR success depending on
	// whether the transport was given the test cert. Inject the server cert.
	if tr, ok := c.client.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // test-only: httptest self-signed
		}
	}
	require.NoError(t, c.Ping())
}

func TestPing_Unreachable(t *testing.T) {
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URL: "http://127.0.0.1:1/",
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	err = c.Ping()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ping")
}

func TestPing_URLTemplateSuccess(t *testing.T) {
	lnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(lnSrv.Close)

	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		// Zero ChangeEvent has empty Table; template still produces a dialable URL
		// by embedding the static host from the test server.
		URLTemplate: lnSrv.URL + "/{{.Table}}",
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.Ping())
}

func TestPing_URLTemplateExecuteError(t *testing.T) {
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URLTemplate: `{{template "missing"}}`,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	// Execute against zero event fails → Ping returns nil.
	require.NoError(t, c.Ping())
}

func TestResolveURL_StaticEmpty(t *testing.T) {
	c := &WebhookSinkConsumer{url: "   "}
	_, err := c.resolveURL(&event.ChangeEvent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is empty")
}

func TestDoRequest_InvalidURL(t *testing.T) {
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{URL: "http://example.com"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	_, _, err = c.doRequest(t.Context(), httpReq{url: "://bad", body: []byte(`{}`), single: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create request")
}

func TestDeliver_URLTemplateExecuteError(t *testing.T) {
	c, err := NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
		URLTemplate: `{{template "missing"}}`,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	err = c.Deliver(t.Context(), eventlog.LogEntry{
		Event: &event.ChangeEvent{Table: "t"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url-template execution")
}

func genSelfSignedPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}
