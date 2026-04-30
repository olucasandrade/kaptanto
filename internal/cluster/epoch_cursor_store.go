package cluster

import (
	"context"
	"log/slog"

	"github.com/olucasandrade/kaptanto/internal/router"
)

// epochCursorStore wraps a ConsumerCursorStore and gates SaveCursor calls on
// partition ownership. A zombie node that reconnects after being replaced holds
// a stale ownership map — its SaveCursor calls for stolen partitions are
// silently dropped, preventing cursor corruption (DLVR-02).
type epochCursorStore struct {
	inner   router.ConsumerCursorStore
	manager *PartitionManager
}

// NewEpochCursorStore returns an epoch-fenced cursor store.
// inner must not be nil. manager must not be nil.
func NewEpochCursorStore(inner router.ConsumerCursorStore, manager *PartitionManager) router.ConsumerCursorStore {
	return &epochCursorStore{inner: inner, manager: manager}
}

// SaveCursor persists the cursor only if this node currently owns partitionID.
// If not owned, the call is silently dropped and nil is returned.
func (e *epochCursorStore) SaveCursor(ctx context.Context, consumerID string, partitionID uint32, seq uint64) error {
	if !e.manager.OwnsPartition(partitionID) {
		slog.Warn("cluster: SaveCursor rejected — partition not owned by this node",
			"partition", partitionID,
			"consumer", consumerID,
		)
		return nil
	}
	return e.inner.SaveCursor(ctx, consumerID, partitionID, seq)
}

// LoadCursor delegates unconditionally to the inner store.
func (e *epochCursorStore) LoadCursor(ctx context.Context, consumerID string, partitionID uint32) (uint64, error) {
	return e.inner.LoadCursor(ctx, consumerID, partitionID)
}
