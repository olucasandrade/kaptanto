package eventlog

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
)

// natsClientHost is the embedded NATS client listener address.
// Always loopback: only the in-process client (via ClientURL) connects.
// External NATS clients are out of scope for --cluster.
const natsClientHost = "127.0.0.1"

// envRefRegex validates STRICT ${VAR} secret references (same style as MCP).
var envRefRegex = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// NatsServerConfig contains configuration for the embedded NATS server.
type NatsServerConfig struct {
	// NodeID is the unique server name — reuses the --node-id flag value.
	// Required for JetStream clustering (Pitfall 5).
	NodeID string

	// ClientPort is the NATS client port. Use -1 for random (tests).
	// Defaults to 4222 if zero. Always bound to 127.0.0.1 (see natsClientHost).
	ClientPort int

	// ClusterPort is the NATS cluster route port for peer-to-peer communication.
	// Defaults to 6222 if zero. Bound for peer reachability (not loopback).
	ClusterPort int

	// Advertise is the "host:port" this node advertises to cluster peers.
	// Only set in cluster mode (len(Peers) > 0).
	Advertise string

	// Peers is the list of peer cluster route addresses e.g. ["node2:6222", "node3:6222"].
	// Empty for single-node or test mode.
	Peers []string

	// StoreDir is the directory for JetStream persistence.
	// Use t.TempDir() in tests.
	StoreDir string

	// SyncAlways must be true for cluster mode to ensure CHK-01 holds after an OS crash.
	// The Jepsen analysis (December 2025) confirmed that the default SyncAlways=false
	// causes up to 14% data loss in coordinated crash scenarios.
	// Safe to set false in unit tests (no OS crash risk in test processes).
	SyncAlways bool

	// RouteUsername is a STRICT ${VAR} env-var reference for cluster route auth.
	// Required (with RoutePassword) when Peers is non-empty. Never a literal secret.
	RouteUsername string

	// RoutePassword is a STRICT ${VAR} env-var reference for cluster route auth.
	// Required (with RouteUsername) when Peers is non-empty. Never a literal secret.
	RoutePassword string

	// RouteTLSCertFile is the path to the PEM certificate for cluster route TLS.
	// Required (with RouteTLSKeyFile) when Peers is non-empty.
	RouteTLSCertFile string

	// RouteTLSKeyFile is the path to the PEM private key for cluster route TLS.
	// Required (with RouteTLSCertFile) when Peers is non-empty.
	RouteTLSKeyFile string
}

// buildNATSServerOptions constructs natssrv.Options from cfg without starting the server.
// The client listener Host is always 127.0.0.1. When Peers is non-empty, route
// username/password (${VAR} refs) and TLS cert+key are required — no silent open mesh.
func buildNATSServerOptions(cfg NatsServerConfig) (*natssrv.Options, error) {
	opts := &natssrv.Options{
		ServerName: cfg.NodeID,
		Host:       natsClientHost,
		Port:       cfg.ClientPort,
		JetStream:  true,
		StoreDir:   cfg.StoreDir,
		// CRITICAL: SyncAlways is a top-level Options field (verified via server/opts.go).
		// With SyncAlways=true NATS fsyncs on every write — required for CHK-01 in cluster mode.
		SyncAlways: cfg.SyncAlways,
		// Suppress NATS banner and logs — kaptanto uses slog for structured logging.
		NoLog:  true,
		NoSigs: true,
	}

	if len(cfg.Peers) == 0 {
		return opts, nil
	}

	user, err := resolveStrictEnvRef(cfg.RouteUsername, "nats cluster route username (--nats-cluster-user)")
	if err != nil {
		return nil, err
	}
	pass, err := resolveStrictEnvRef(cfg.RoutePassword, "nats cluster route password (--nats-cluster-password)")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.RouteTLSCertFile) == "" || strings.TrimSpace(cfg.RouteTLSKeyFile) == "" {
		return nil, fmt.Errorf("nats: cluster route TLS cert and key are required when --cluster-peers is set (--nats-cluster-tls-cert / --nats-cluster-tls-key)")
	}
	routeTLS, err := buildClusterRouteTLS(cfg.RouteTLSCertFile, cfg.RouteTLSKeyFile)
	if err != nil {
		return nil, err
	}

	routes := make([]*url.URL, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		u := &url.URL{Scheme: "nats", Host: peer}
		routes = append(routes, u)
	}

	opts.Cluster = natssrv.ClusterOpts{
		Name:      "kaptanto", // shared cluster name constant across all nodes
		Port:      cfg.ClusterPort,
		Advertise: cfg.Advertise,
		Username:  user,
		Password:  pass,
		TLSConfig: routeTLS,
	}
	opts.Routes = routes
	return opts, nil
}

// buildClusterRouteTLS loads a shared cert+key for encrypted cluster routes.
// The leaf is trusted as both client and server CA so peers sharing the same
// material can verify each other. Peer hostnames in --cluster-peers / Advertise
// must appear in the certificate SAN (or CN) for handshake success.
func buildClusterRouteTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("nats: load cluster route TLS cert/key: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("nats: cluster route TLS cert has no certificates")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("nats: parse cluster route TLS cert: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func resolveStrictEnvRef(raw, field string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("nats: %s is required when --cluster-peers is set and must be an environment variable reference like ${VAR}", field)
	}
	if !envRefRegex.MatchString(trimmed) {
		return "", fmt.Errorf("nats: %s must be an environment variable reference like ${VAR}", field)
	}
	varName := trimmed[2 : len(trimmed)-1]
	val, ok := os.LookupEnv(varName)
	if !ok || val == "" {
		return "", fmt.Errorf("nats: %s references ${%s} which is unset", field, varName)
	}
	return val, nil
}

// startEmbeddedNATS starts an in-process NATS server with JetStream enabled using the
// provided configuration. It blocks until the server is ready to accept connections
// (up to 10 seconds) or returns an error.
//
// CRITICAL: SyncAlways is a top-level field on server.Options — NOT inside any
// JetStreamConfig sub-struct. Setting it elsewhere silently breaks CHK-01.
//
// For single-node mode (no peers / tests), the Cluster block is omitted entirely.
// For cluster mode (len(cfg.Peers) > 0), Cluster.Name, Routes, route auth, and
// route TLS are set. The client listener is always bound to 127.0.0.1.
func startEmbeddedNATS(cfg NatsServerConfig) (*natssrv.Server, error) {
	opts, err := buildNATSServerOptions(cfg)
	if err != nil {
		return nil, err
	}

	ns, err := natssrv.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("nats: new server: %w", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		return nil, fmt.Errorf("nats: server not ready within 10s")
	}

	return ns, nil
}
