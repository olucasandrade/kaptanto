package eventlog

import (
	"fmt"
	"net/url"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
)

// NatsServerConfig contains configuration for the embedded NATS server.
type NatsServerConfig struct {
	// NodeID is the unique server name — reuses the --node-id flag value.
	// Required for JetStream clustering (Pitfall 5).
	NodeID string

	// ClientPort is the NATS client port. Use -1 for random (tests).
	// Defaults to 4222 if zero.
	ClientPort int

	// ClusterPort is the NATS cluster route port for peer-to-peer communication.
	// Defaults to 6222 if zero.
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
}

// startEmbeddedNATS starts an in-process NATS server with JetStream enabled using the
// provided configuration. It blocks until the server is ready to accept connections
// (up to 10 seconds) or returns an error.
//
// CRITICAL: SyncAlways is a top-level field on server.Options — NOT inside any
// JetStreamConfig sub-struct. Setting it elsewhere silently breaks CHK-01.
//
// For single-node mode (no peers / tests), the Cluster block is omitted entirely.
// For cluster mode (len(cfg.Peers) > 0), Cluster.Name and Routes are set.
func startEmbeddedNATS(cfg NatsServerConfig) (*natssrv.Server, error) {
	opts := &natssrv.Options{
		ServerName: cfg.NodeID,
		Port:       cfg.ClientPort,
		JetStream:  true,
		StoreDir:   cfg.StoreDir,
		// CRITICAL: SyncAlways is a top-level Options field (verified via server/opts.go).
		// With SyncAlways=true NATS fsyncs on every write — required for CHK-01 in cluster mode.
		SyncAlways: cfg.SyncAlways,
		// Suppress NATS banner and logs — kaptanto uses slog for structured logging.
		NoLog:    true,
		NoSigs:   true,
	}

	if len(cfg.Peers) > 0 {
		// Parse peer addresses into []*url.URL for cluster routing.
		routes := make([]*url.URL, 0, len(cfg.Peers))
		for _, peer := range cfg.Peers {
			u := &url.URL{Scheme: "nats", Host: peer}
			routes = append(routes, u)
		}

		opts.Cluster = natssrv.ClusterOpts{
			Name:      "kaptanto", // shared cluster name constant across all nodes
			Port:      cfg.ClusterPort,
			Advertise: cfg.Advertise,
		}
		opts.Routes = routes
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
