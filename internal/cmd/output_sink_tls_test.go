package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeObsSink is a minimal messageSink for testing the observability HTTP
// server without bringing up a real broker connection.
type fakeObsSink struct{}

func (f *fakeObsSink) ID() string                                           { return "fake" }
func (f *fakeObsSink) Deliver(_ context.Context, _ eventlog.LogEntry) error { return nil }
func (f *fakeObsSink) SetMetrics(_ *observability.KaptantoMetrics)          {}
func (f *fakeObsSink) Ping() error                                          { return nil }
func (f *fakeObsSink) Close()                                               {}

var _ messageSink = (*fakeObsSink)(nil)

func newLocalListener(t *testing.T) (int, net.Listener) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	return lis.Addr().(*net.TCPAddr).Port, lis
}

func waitForObsServer(t *testing.T, addr string, client *http.Client) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("%s/healthz", addr))
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("observability server did not start on %s", addr)
}

// generateServerCertSignedByCA creates a server cert/key pair signed by the
// provided CA and writes them to dir. It returns the file paths.
func generateServerCertSignedByCA(t *testing.T, dir, caFile string, caKey *ecdsa.PrivateKey) (certFile, keyFile string) {
	t.Helper()

	caPEM, err := os.ReadFile(caFile)
	require.NoError(t, err)
	block, _ := pem.Decode(caPEM)
	require.NotNil(t, block)
	caCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "kaptanto-server"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	require.NoError(t, err)

	certFile = filepath.Join(dir, "server-cert.pem")
	keyFile = filepath.Join(dir, "server-key.pem")

	cf, err := os.Create(certFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, cf.Close())

	privDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	kf, err := os.Create(keyFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}))
	require.NoError(t, kf.Close())

	return certFile, keyFile
}

// generateClientCertSignedByCA creates a client cert/key pair signed by the
// provided CA and returns the file paths.
func generateClientCertSignedByCA(t *testing.T, dir, caFile string, caKey *ecdsa.PrivateKey) (certFile, keyFile string) {
	t.Helper()

	caPEM, err := os.ReadFile(caFile)
	require.NoError(t, err)
	block, _ := pem.Decode(caPEM)
	require.NotNil(t, block)
	caCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(4),
		Subject:      pkix.Name{CommonName: "kaptanto-client"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	require.NoError(t, err)

	certFile = filepath.Join(dir, "client-cert.pem")
	keyFile = filepath.Join(dir, "client-key.pem")

	cf, err := os.Create(certFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	require.NoError(t, cf.Close())

	privDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	kf, err := os.Create(keyFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}))
	require.NoError(t, kf.Close())

	return certFile, keyFile
}

func TestBuildOutputServer_SinkRequiresTLS(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "kafka"
	cfg.AuthToken = "token"
	cfg.Sinks.Kafka = &config.KafkaSinkConfig{
		BootstrapServers: []string{"localhost:1"},
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires TLS")
}

func TestBuildOutputServer_SinkInsecureAllowed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "kafka"
	cfg.AuthToken = ""
	cfg.Insecure = true
	cfg.Sinks.Kafka = &config.KafkaSinkConfig{
		BootstrapServers: []string{"localhost:1"},
		TopicTemplate:    "cdc.{{.Schema}}.{{.Table}}",
	}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	fn, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, fn)
}

func TestBuildSinkServer_TLSObservability(t *testing.T) {
	const token = "obs-token"
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	cfg := config.Defaults()
	port, lis := newLocalListener(t)
	cfg.Port = port
	cfg.AuthToken = token
	cfg.ServerTLS = config.ServerTLSConfig{CertFile: certFile, KeyFile: keyFile}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())
	fn, err := buildSinkServer(cfg, "fake", &fakeObsSink{}, rtr, metrics, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), lis)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fn(ctx) }()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	addr := fmt.Sprintf("https://127.0.0.1:%d", cfg.Port)
	waitForObsServer(t, addr, client)

	// No token → 401.
	resp, err := client.Get(addr + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	// Valid token → 200.
	req, err := http.NewRequest(http.MethodGet, addr+"/healthz", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	cancel()
}

func TestBuildSinkServer_mTLSObservability(t *testing.T) {
	const token = "mtls-token"
	dir := t.TempDir()
	var caFile string

	// Generate a fresh CA pair and write the cert to disk so server/client certs
	// can be signed and verified against it.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caCertDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(10),
		Subject:               pkix.Name{CommonName: "kaptanto-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, &x509.Certificate{
		SerialNumber:          big.NewInt(10),
		Subject:               pkix.Name{CommonName: "kaptanto-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	_, err = x509.ParseCertificate(caCertDER)
	require.NoError(t, err)
	caFile = filepath.Join(dir, "mtls-ca.pem")
	f, err := os.Create(caFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: caCertDER}))
	require.NoError(t, f.Close())

	serverCert, serverKey := generateServerCertSignedByCA(t, dir, caFile, caKey)
	clientCert, clientKey := generateClientCertSignedByCA(t, dir, caFile, caKey)

	cfg := config.Defaults()
	port, lis := newLocalListener(t)
	cfg.Port = port
	cfg.AuthToken = token
	cfg.ServerTLS = config.ServerTLSConfig{
		CertFile:     serverCert,
		KeyFile:      serverKey,
		ClientCAFile: caFile,
	}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())
	fn, err := buildSinkServer(cfg, "fake", &fakeObsSink{}, rtr, metrics, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), lis)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fn(ctx) }()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER}))

	clientCertTLS, err := tls.LoadX509KeyPair(clientCert, clientKey)
	require.NoError(t, err)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{clientCertTLS},
				ServerName:   "localhost",
			},
		},
	}
	addr := fmt.Sprintf("https://127.0.0.1:%d", cfg.Port)
	waitForObsServer(t, addr, client)

	// Client with cert and token → 200.
	req, err := http.NewRequest(http.MethodGet, addr+"/healthz", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Client without cert should fail the TLS handshake.
	noCertClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: "localhost",
			},
		},
	}
	req2, err := http.NewRequest(http.MethodGet, addr+"/healthz", nil)
	require.NoError(t, err)
	req2.Header.Set("Authorization", "Bearer "+token)
	_, err = noCertClient.Do(req2)
	require.Error(t, err, "client without certificate must fail mTLS handshake")

	cancel()
}

func TestBuildOutputServer_WebhookRequiresSinkConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "webhook"
	cfg.Insecure = true

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sinks.webhook")
}

func TestBuildOutputServer_WebhookRequiresTLS(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "webhook"
	cfg.AuthToken = "token"
	cfg.Sinks.Webhook = &config.WebhookSinkConfig{
		URL: "http://localhost:1",
	}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires TLS")
}

func TestBuildOutputServer_WebhookStarts(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "webhook"
	cfg.Insecure = true
	cfg.Sinks.Webhook = &config.WebhookSinkConfig{
		URL: "http://localhost:1",
	}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	fn, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, fn)
}

func TestBuildOutputServer_NoneObservabilityRequiresTLS(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "none"
	cfg.AuthToken = "tls-check-token"

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	_, err := buildOutputServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.NotFoundHandler(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires TLS")
}

func TestBuildObservabilityServer_ServesEndpoints(t *testing.T) {
	const token = "obs-none-token"
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	cfg := config.Defaults()
	port, lis := newLocalListener(t)
	cfg.Port = port
	_ = lis.Close() // release the port so the observability server can bind
	cfg.AuthToken = token
	cfg.ServerTLS = config.ServerTLSConfig{CertFile: certFile, KeyFile: keyFile}

	metrics := observability.NewKaptantoMetrics()

	fn, err := buildObservabilityServer(cfg, metrics, []observability.HealthProbe{
		{Name: "test", Check: func() error { return nil }},
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fn(ctx) }()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	addr := fmt.Sprintf("https://127.0.0.1:%d", cfg.Port)
	waitForObsServer(t, addr, client)

	// No token → 401.
	resp, err := client.Get(addr + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	// Token → 200 on all three observability endpoints.
	for _, path := range []string{"/healthz", "/metrics", "/openapi.json"} {
		req, err := http.NewRequest(http.MethodGet, addr+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		_ = resp.Body.Close()
	}

	cancel()
}

func TestBuildGRPCServer_ObservabilityTLS(t *testing.T) {
	const token = "grpc-obs-token"
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	cfg := config.Defaults()
	cfg.Output = "grpc"
	grpcPort, grpcLis := newLocalListener(t)
	_ = grpcLis.Close()
	cfg.Port = grpcPort
	_, obsLis := newLocalListener(t)
	cfg.AuthToken = token
	cfg.ServerTLS = config.ServerTLSConfig{CertFile: certFile, KeyFile: keyFile}

	metrics := observability.NewKaptantoMetrics()
	rtr := router.NewRouter(nil, 1, router.NewNoopCursorStore())

	fn, err := buildGRPCServer(cfg, rtr, router.NewNoopCursorStore(), metrics, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), nil, nil, obsLis)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fn(ctx) }()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	addr := fmt.Sprintf("https://%s", obsLis.Addr().String())
	waitForObsServer(t, addr, client)

	// Without token → 401.
	resp, err := client.Get(addr + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	// With token → 200.
	req, err := http.NewRequest(http.MethodGet, addr+"/healthz", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	cancel()
}
