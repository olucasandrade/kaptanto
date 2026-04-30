package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
)

const createPartitionsTableSQL = `
CREATE TABLE IF NOT EXISTS kaptanto_partitions (
    partition_id  INTEGER     PRIMARY KEY,
    owner_node_id TEXT        NULL,
    epoch         BIGINT      NOT NULL DEFAULT 0,
    claimed_at    TIMESTAMPTZ NULL
);
INSERT INTO kaptanto_partitions (partition_id)
SELECT generate_series(0, 63)
ON CONFLICT DO NOTHING;`

const listUnclaimedSQL = `
SELECT partition_id FROM kaptanto_partitions
WHERE owner_node_id IS NULL
ORDER BY partition_id;`

const claimPartitionSQL = `
UPDATE kaptanto_partitions
SET owner_node_id = $1, epoch = epoch + 1, claimed_at = NOW()
WHERE partition_id = $2 AND owner_node_id IS NULL
RETURNING partition_id, epoch;`

const stealPartitionsSQL = `
UPDATE kaptanto_partitions
SET owner_node_id = $1, epoch = epoch + 1, claimed_at = NOW()
WHERE owner_node_id = $2
RETURNING partition_id, epoch;`

const releasePartitionsSQL = `
UPDATE kaptanto_partitions
SET owner_node_id = NULL, claimed_at = NULL
WHERE owner_node_id = $1;`

const listOwnedSQL = `
SELECT partition_id, epoch FROM kaptanto_partitions
WHERE owner_node_id = $1
ORDER BY partition_id;`

// PartitionClaim represents a single partition successfully owned by this node,
// along with the epoch at the time of ownership.
type PartitionClaim struct {
	PartitionID uint32
	Epoch       int64
}

// PartitionStore manages ownership of the 64 fixed rows in the
// kaptanto_partitions Postgres table. It provides atomic claim, steal, and
// release operations that prevent split-brain dual ownership.
type PartitionStore struct {
	conn   *pgx.Conn
	nodeID string
	mu     sync.RWMutex
	epochs map[uint32]int64 // partitionID → epoch currently held by this node
}

// OpenPartitionStore connects to Postgres at dsn, auto-creates and seeds the
// kaptanto_partitions table (64 rows, 0-63), and returns a ready *PartitionStore.
func OpenPartitionStore(ctx context.Context, dsn, nodeID string) (*PartitionStore, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("cluster: open postgres for partition store: %w", err)
	}

	if _, err := conn.Exec(ctx, createPartitionsTableSQL); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("cluster: create kaptanto_partitions table: %w", err)
	}

	return &PartitionStore{
		conn:   conn,
		nodeID: nodeID,
		epochs: make(map[uint32]int64),
	}, nil
}

// ClaimUnclaimed attempts to atomically claim every currently unclaimed
// partition. Partitions won by a concurrent node (pgx.ErrNoRows on the UPDATE
// RETURNING) are silently skipped — that is a normal race loss, not an error.
// Returns a non-nil slice of successfully claimed partitions.
func (ps *PartitionStore) ClaimUnclaimed(ctx context.Context) ([]PartitionClaim, error) {
	rows, err := ps.conn.Query(ctx, listUnclaimedSQL)
	if err != nil {
		return nil, fmt.Errorf("cluster: list unclaimed partitions: %w", err)
	}
	defer rows.Close()

	var pids []uint32
	for rows.Next() {
		var pid uint32
		if err := rows.Scan(&pid); err != nil {
			return nil, fmt.Errorf("cluster: scan unclaimed partition_id: %w", err)
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cluster: unclaimed rows: %w", err)
	}

	claimed := []PartitionClaim{} // non-nil empty slice
	for _, pid := range pids {
		var claimedID uint32
		var epoch int64
		err := ps.conn.QueryRow(ctx, claimPartitionSQL, ps.nodeID, pid).Scan(&claimedID, &epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			// Another node won the race for this partition — skip silently.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cluster: claim partition %d: %w", pid, err)
		}

		ps.mu.Lock()
		ps.epochs[claimedID] = epoch
		ps.mu.Unlock()

		claimed = append(claimed, PartitionClaim{PartitionID: claimedID, Epoch: epoch})
	}

	return claimed, nil
}

// StealStalePartitions atomically reassigns all partitions currently owned by
// staleNodeID to this node in a single round-trip. Returns a non-nil slice of
// stolen partitions.
func (ps *PartitionStore) StealStalePartitions(ctx context.Context, staleNodeID string) ([]PartitionClaim, error) {
	rows, err := ps.conn.Query(ctx, stealPartitionsSQL, ps.nodeID, staleNodeID)
	if err != nil {
		return nil, fmt.Errorf("cluster: steal partitions from %q: %w", staleNodeID, err)
	}
	defer rows.Close()

	stolen := []PartitionClaim{} // non-nil empty slice
	for rows.Next() {
		var pid uint32
		var epoch int64
		if err := rows.Scan(&pid, &epoch); err != nil {
			return nil, fmt.Errorf("cluster: scan stolen partition: %w", err)
		}
		ps.mu.Lock()
		ps.epochs[pid] = epoch
		ps.mu.Unlock()

		stolen = append(stolen, PartitionClaim{PartitionID: pid, Epoch: epoch})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cluster: steal partitions rows: %w", err)
	}

	return stolen, nil
}

// ReleaseAll sets owner_node_id = NULL for all partitions owned by this node,
// enabling graceful handoff to other cluster members on shutdown.
func (ps *PartitionStore) ReleaseAll(ctx context.Context) error {
	if _, err := ps.conn.Exec(ctx, releasePartitionsSQL, ps.nodeID); err != nil {
		return fmt.Errorf("cluster: release partitions for %q: %w", ps.nodeID, err)
	}

	ps.mu.Lock()
	ps.epochs = make(map[uint32]int64)
	ps.mu.Unlock()

	return nil
}

// ListOwned queries the current partition ownership state from Postgres and
// returns all partitions owned by this node. Always returns a non-nil slice.
func (ps *PartitionStore) ListOwned(ctx context.Context) ([]PartitionClaim, error) {
	rows, err := ps.conn.Query(ctx, listOwnedSQL, ps.nodeID)
	if err != nil {
		return nil, fmt.Errorf("cluster: list owned partitions: %w", err)
	}
	defer rows.Close()

	owned := []PartitionClaim{} // explicitly non-nil empty slice
	for rows.Next() {
		var pid uint32
		var epoch int64
		if err := rows.Scan(&pid, &epoch); err != nil {
			return nil, fmt.Errorf("cluster: scan owned partition: %w", err)
		}
		owned = append(owned, PartitionClaim{PartitionID: pid, Epoch: epoch})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cluster: owned partitions rows: %w", err)
	}

	return owned, nil
}

// EpochFor returns the epoch of the given partition if this node currently
// tracks it in its in-memory ownership map. Returns (0, false) if unknown.
func (ps *PartitionStore) EpochFor(partitionID uint32) (int64, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	epoch, ok := ps.epochs[partitionID]
	return epoch, ok
}

// Close releases the underlying Postgres connection.
func (ps *PartitionStore) Close(ctx context.Context) error {
	if err := ps.conn.Close(ctx); err != nil {
		return fmt.Errorf("cluster: close partition store: %w", err)
	}
	return nil
}
