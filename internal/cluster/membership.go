// Package cluster provides cluster membership management for kaptanto HA mode.
package cluster

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

const createNodesTableSQL = `
CREATE TABLE IF NOT EXISTS kaptanto_nodes (
    node_id               TEXT        PRIMARY KEY,
    address               TEXT        NOT NULL,
    last_seen             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    partition_assignments JSONB       NOT NULL DEFAULT '[]'::jsonb
);`

const upsertNodeSQL = `
INSERT INTO kaptanto_nodes (node_id, address, last_seen, partition_assignments)
VALUES ($1, $2, NOW(), '[]'::jsonb)
ON CONFLICT (node_id) DO UPDATE
    SET address = EXCLUDED.address, last_seen = NOW();`

const deleteNodeSQL = `DELETE FROM kaptanto_nodes WHERE node_id = $1;`

const staleNodesSQL = `
SELECT node_id FROM kaptanto_nodes
WHERE last_seen < NOW() - ($1 * INTERVAL '1 second');`

// NodeHeartbeater maintains the node's membership record in the kaptanto_nodes
// Postgres table. It upserts on start and on every interval tick. On graceful
// shutdown (context cancellation) it deletes its own row to signal departure.
type NodeHeartbeater struct {
	conn           *pgx.Conn
	nodeID         string
	address        string
	interval       time.Duration
	staleThreshold int
}

// OpenNodeHeartbeater connects to Postgres at dsn, auto-creates the
// kaptanto_nodes table, and returns a ready *NodeHeartbeater.
//
// If nodeID is empty, it is derived from os.Hostname() with a fallback to
// "node-unknown". The caller is responsible for passing a formatted address
// (e.g., fmt.Sprintf("%s:%d", hostname, port)).
func OpenNodeHeartbeater(ctx context.Context, dsn, nodeID, address string, interval time.Duration, staleThreshold int) (*NodeHeartbeater, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("cluster: open postgres: %w", err)
	}

	if _, err := conn.Exec(ctx, createNodesTableSQL); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("cluster: create kaptanto_nodes table: %w", err)
	}

	id := deriveNodeID(nodeID)

	return &NodeHeartbeater{
		conn:           conn,
		nodeID:         id,
		address:        address,
		interval:       interval,
		staleThreshold: staleThreshold,
	}, nil
}

// Run upserts the node record immediately, then on every interval tick.
// When ctx is cancelled (graceful shutdown), it calls markOffline and returns.
// Run is designed to be called in a goroutine.
func (h *NodeHeartbeater) Run(ctx context.Context) {
	h.upsert(ctx) //nolint:errcheck // best-effort on first upsert; log if needed
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.markOffline(context.Background()) //nolint:errcheck // markOffline uses background ctx so DELETE executes after cancellation
			return
		case <-ticker.C:
			h.upsert(ctx) //nolint:errcheck // ticker upserts are best-effort; connection errors are observable via health check
		}
	}
}

// StaleNodes returns the node_ids of nodes whose last_seen timestamp is older
// than thresholdSeconds ago. It always returns a non-nil slice (empty when
// no stale nodes exist).
func (h *NodeHeartbeater) StaleNodes(ctx context.Context, thresholdSeconds int) ([]string, error) {
	rows, err := h.conn.Query(ctx, staleNodesSQL, thresholdSeconds)
	if err != nil {
		return nil, fmt.Errorf("cluster: stale nodes query: %w", err)
	}
	defer rows.Close()

	nodeIDs := []string{} // explicitly non-nil empty slice
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cluster: scan node_id: %w", err)
		}
		nodeIDs = append(nodeIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cluster: stale nodes rows: %w", err)
	}

	return nodeIDs, nil
}

// NodeID returns the node identifier in use by this heartbeater.
func (h *NodeHeartbeater) NodeID() string {
	return h.nodeID
}

// Close releases the pgx connection. Must be called on graceful shutdown.
func (h *NodeHeartbeater) Close(ctx context.Context) error {
	if err := h.conn.Close(ctx); err != nil {
		return fmt.Errorf("cluster: close heartbeater: %w", err)
	}
	return nil
}

// upsert writes (or refreshes) the node record in kaptanto_nodes.
func (h *NodeHeartbeater) upsert(ctx context.Context) error {
	if _, err := h.conn.Exec(ctx, upsertNodeSQL, h.nodeID, h.address); err != nil {
		return fmt.Errorf("cluster: upsert node %q: %w", h.nodeID, err)
	}
	return nil
}

// markOffline deletes the node row from kaptanto_nodes, signalling a graceful
// shutdown to other cluster members. It intentionally accepts context.Background()
// so the DELETE executes even after the main context is cancelled.
func (h *NodeHeartbeater) markOffline(ctx context.Context) error {
	if _, err := h.conn.Exec(ctx, deleteNodeSQL, h.nodeID); err != nil {
		return fmt.Errorf("cluster: mark offline %q: %w", h.nodeID, err)
	}
	return nil
}

// deriveNodeID returns nodeID if non-empty; otherwise derives from os.Hostname()
// with a fallback to "node-unknown".
func deriveNodeID(nodeID string) string {
	if nodeID != "" {
		return nodeID
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return "node-unknown"
	}
	return hostname
}
