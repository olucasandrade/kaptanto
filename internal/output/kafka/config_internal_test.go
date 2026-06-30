// White-box tests for the unexported config builders (buildSASLMechanism,
// buildTLSConfig). These are pure logic and don't need a broker.
package kafkasink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
)

func TestBuildSASLMechanism(t *testing.T) {
	for _, mech := range []string{"PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"} {
		t.Run(mech, func(t *testing.T) {
			m, err := buildSASLMechanism(config.KafkaSinkConfig{
				SASLMechanism: mech,
				SASLUsername:  "u",
				SASLPassword:  "p",
			})
			require.NoError(t, err)
			assert.NotNil(t, m)
		})
	}
}

func TestBuildSASLMechanism_Unknown(t *testing.T) {
	_, err := buildSASLMechanism(config.KafkaSinkConfig{SASLMechanism: "plain"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown sasl-mechanism")
}

func TestBuildTLSConfig_Empty(t *testing.T) {
	cfg, err := buildTLSConfig(config.TLSConfig{})
	require.NoError(t, err)
	assert.Nil(t, cfg.RootCAs)
	assert.Empty(t, cfg.Certificates)
}

func TestBuildTLSConfig_CAFile(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := genSelfSignedPEM(t)
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, certPEM, 0o600))

	cfg, err := buildTLSConfig(config.TLSConfig{CAFile: caPath})
	require.NoError(t, err)
	assert.NotNil(t, cfg.RootCAs)

	// mTLS: also load the client key pair.
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

// genSelfSignedPEM returns a self-signed cert + matching key, both PEM-encoded.
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
