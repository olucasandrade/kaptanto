package cmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// generateSelfSignedCert writes a self-signed TLS cert+key pair to dir and
// returns the cert and key file paths.
func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kaptanto-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         false,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	cf, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	defer cf.Close()
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("encode cert: %v", err)
	}

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	kf, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	defer kf.Close()
	if err := pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}); err != nil {
		t.Fatalf("encode key: %v", err)
	}

	return certFile, keyFile
}

// TestBuildServerTLSConfig_NilWhenEmpty verifies that no cert/key produces nil (plaintext mode).
func TestBuildServerTLSConfig_NilWhenEmpty(t *testing.T) {
	tlsCfg, err := buildServerTLSConfig(config.ServerTLSConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg != nil {
		t.Fatalf("expected nil tlsConfig for empty ServerTLSConfig, got %+v", tlsCfg)
	}
}

// TestBuildServerTLSConfig_ErrorOnPartialConfig verifies that providing only
// cert or only key returns an error.
func TestBuildServerTLSConfig_ErrorOnPartialConfig(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	for _, tc := range []struct {
		name string
		cfg  config.ServerTLSConfig
	}{
		{"cert only", config.ServerTLSConfig{CertFile: certFile}},
		{"key only", config.ServerTLSConfig{KeyFile: keyFile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildServerTLSConfig(tc.cfg)
			if err == nil {
				t.Fatal("expected error for partial TLS config, got nil")
			}
		})
	}
}

// TestBuildServerTLSConfig_ValidCertKey verifies that a valid cert/key pair
// builds a non-nil *tls.Config with MinVersion = TLS 1.2 and no mTLS.
func TestBuildServerTLSConfig_ValidCertKey(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	tlsCfg, err := buildServerTLSConfig(config.ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil *tls.Config")
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want %d (TLS 1.2)", tlsCfg.MinVersion, tls.VersionTLS12)
	}
	if tlsCfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v, want NoClientCert (mTLS not requested)", tlsCfg.ClientAuth)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(tlsCfg.Certificates))
	}
}

// TestBuildServerTLSConfig_WithClientCA verifies mTLS is enabled when
// ClientCAFile is provided alongside cert/key.
func TestBuildServerTLSConfig_WithClientCA(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	// Reuse the same self-signed cert as a CA for simplicity.
	tlsCfg, err := buildServerTLSConfig(config.ServerTLSConfig{
		CertFile:     certFile,
		KeyFile:      keyFile,
		ClientCAFile: certFile, // self-signed cert also acts as its own CA
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil *tls.Config")
	}
	if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", tlsCfg.ClientAuth)
	}
	if tlsCfg.ClientCAs == nil {
		t.Fatal("ClientCAs pool must not be nil when ClientCAFile is set")
	}
}

// TestBuildServerTLSConfig_MissingCertFile verifies that a nonexistent cert
// file returns an error.
func TestBuildServerTLSConfig_MissingCertFile(t *testing.T) {
	_, err := buildServerTLSConfig(config.ServerTLSConfig{
		CertFile: "/nonexistent/cert.pem",
		KeyFile:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatal("expected error for missing cert/key files, got nil")
	}
}

// TestRequireServerTLS_TLSConfigured verifies no error when TLS is provided.
func TestRequireServerTLS_TLSConfigured(t *testing.T) {
	err := requireServerTLS("sse", &tls.Config{}, false)
	if err != nil {
		t.Fatalf("unexpected error when TLS is configured: %v", err)
	}
}

// TestRequireServerTLS_InsecureAllowed verifies that insecure=true allows
// plaintext and returns no error.
func TestRequireServerTLS_InsecureAllowed(t *testing.T) {
	err := requireServerTLS("sse", nil, true)
	if err != nil {
		t.Fatalf("expected no error for insecure mode, got: %v", err)
	}
}

// TestRequireServerTLS_NeitherTLSNorInsecure verifies that missing TLS
// configuration without --insecure returns a descriptive error.
func TestRequireServerTLS_NeitherTLSNorInsecure(t *testing.T) {
	for _, output := range []string{"sse", "grpc"} {
		t.Run(output, func(t *testing.T) {
			err := requireServerTLS(output, nil, false)
			if err == nil {
				t.Fatalf("expected error for plaintext %s without --insecure, got nil", output)
			}
		})
	}
}

// TestBuildOutputServer_SSE_RequiresTLS verifies that buildOutputServer returns
// an error for SSE output when no TLS is configured and --insecure is not set.
func TestBuildOutputServer_SSE_RequiresTLS(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "sse"
	// No ServerTLS set, Insecure = false (default)

	_, err := buildOutputServer(cfg, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error: sse without TLS and without --insecure")
	}
}

// TestBuildOutputServer_gRPC_RequiresTLS verifies that buildOutputServer
// returns an error for gRPC output when no TLS is configured and --insecure
// is not set.
func TestBuildOutputServer_gRPC_RequiresTLS(t *testing.T) {
	cfg := config.Defaults()
	cfg.Output = "grpc"
	// No ServerTLS set, Insecure = false (default)

	_, err := buildOutputServer(cfg, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error: grpc without TLS and without --insecure")
	}
}

// TestTLSPolicy_SSE_InsecureAllowed verifies that --insecure bypasses
// the TLS requirement for SSE output by testing requireServerTLS directly.
func TestTLSPolicy_SSE_InsecureAllowed(t *testing.T) {
	err := requireServerTLS("sse", nil /* no TLS */, true /* insecure */)
	if err != nil {
		t.Fatalf("expected no error for --insecure sse, got: %v", err)
	}
}

// TestTLSPolicy_gRPC_InsecureAllowed verifies that --insecure bypasses
// the TLS requirement for gRPC output by testing requireServerTLS directly.
func TestTLSPolicy_gRPC_InsecureAllowed(t *testing.T) {
	err := requireServerTLS("grpc", nil /* no TLS */, true /* insecure */)
	if err != nil {
		t.Fatalf("expected no error for --insecure grpc, got: %v", err)
	}
}

// TestTLSPolicy_WithCert_NoInsecureNeeded verifies that a valid TLS config
// makes --insecure irrelevant: TLS takes precedence and no error is returned.
func TestTLSPolicy_WithCert_NoInsecureNeeded(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	tlsCfg, err := buildServerTLSConfig(config.ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("build tls config: %v", err)
	}

	for _, output := range []string{"sse", "grpc"} {
		t.Run(output, func(t *testing.T) {
			if err := requireServerTLS(output, tlsCfg, false); err != nil {
				t.Fatalf("unexpected error when TLS is configured: %v", err)
			}
		})
	}
}
