package eventlog

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
)

func writeTempRouteTLSPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kaptanto-cluster-route"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "route.crt")
	keyPath = filepath.Join(dir, "route.key")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certPath, keyPath
}

func TestBuildNATSServerOptions_LoopbackHost(t *testing.T) {
	opts, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:     "node-a",
		ClientPort: -1,
		StoreDir:   t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", opts.Host)
	assert.Empty(t, opts.Cluster.Username)
	assert.Nil(t, opts.Cluster.TLSConfig)
}

func TestBuildNATSServerOptions_RejectsMissingRouteCredentials(t *testing.T) {
	cert, key := writeTempRouteTLSPair(t)
	_, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:           "node-a",
		ClientPort:       -1,
		ClusterPort:      -1,
		Peers:            []string{"127.0.0.1:6222"},
		StoreDir:         t.TempDir(),
		RouteTLSCertFile: cert,
		RouteTLSKeyFile:  key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats-cluster-user")
}

func TestBuildNATSServerOptions_RejectsLiteralRouteCredentials(t *testing.T) {
	cert, key := writeTempRouteTLSPair(t)
	_, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:           "node-a",
		ClientPort:       -1,
		ClusterPort:      -1,
		Peers:            []string{"127.0.0.1:6222"},
		StoreDir:         t.TempDir(),
		RouteUsername:    "not-an-env-ref",
		RoutePassword:    "${NATS_CLUSTER_TEST_PASS}",
		RouteTLSCertFile: cert,
		RouteTLSKeyFile:  key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${VAR}")
}

func TestBuildNATSServerOptions_RejectsMissingTLS(t *testing.T) {
	t.Setenv("NATS_CLUSTER_TEST_USER", "route-user")
	t.Setenv("NATS_CLUSTER_TEST_PASS", "route-pass")
	_, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:        "node-a",
		ClientPort:    -1,
		ClusterPort:   -1,
		Peers:         []string{"127.0.0.1:6222"},
		StoreDir:      t.TempDir(),
		RouteUsername: "${NATS_CLUSTER_TEST_USER}",
		RoutePassword: "${NATS_CLUSTER_TEST_PASS}",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats-cluster-tls-cert")
}

func TestBuildNATSServerOptions_ClusterRouteAuthAndTLS(t *testing.T) {
	t.Setenv("NATS_CLUSTER_TEST_USER", "route-user")
	t.Setenv("NATS_CLUSTER_TEST_PASS", "route-pass")
	cert, key := writeTempRouteTLSPair(t)

	opts, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:           "node-a",
		ClientPort:       -1,
		ClusterPort:      6222,
		Advertise:        "node-a:6222",
		Peers:            []string{"node-b:6222"},
		StoreDir:         t.TempDir(),
		RouteUsername:    "${NATS_CLUSTER_TEST_USER}",
		RoutePassword:    "${NATS_CLUSTER_TEST_PASS}",
		RouteTLSCertFile: cert,
		RouteTLSKeyFile:  key,
	})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", opts.Host)
	assert.Equal(t, "route-user", opts.Cluster.Username)
	assert.Equal(t, "route-pass", opts.Cluster.Password)
	assert.NotEmpty(t, opts.Cluster.Username)
	assert.NotEmpty(t, opts.Cluster.Password)
	require.NotNil(t, opts.Cluster.TLSConfig)
	assert.NotEmpty(t, opts.Cluster.TLSConfig.Certificates)
	assert.Equal(t, "kaptanto", opts.Cluster.Name)
	require.Len(t, opts.Routes, 1)
	assert.Equal(t, "node-b:6222", opts.Routes[0].Host)
}

func TestBuildNATSServerOptions_UnsetEnvRef(t *testing.T) {
	cert, key := writeTempRouteTLSPair(t)
	_, err := buildNATSServerOptions(NatsServerConfig{
		NodeID:           "node-a",
		Peers:            []string{"127.0.0.1:6222"},
		StoreDir:         t.TempDir(),
		RouteUsername:    "${NATS_CLUSTER_UNSET_USER_XYZ}",
		RoutePassword:    "${NATS_CLUSTER_UNSET_PASS_XYZ}",
		RouteTLSCertFile: cert,
		RouteTLSKeyFile:  key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unset")
}
