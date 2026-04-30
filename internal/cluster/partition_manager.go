package cluster

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/router"
)

// PartitionManager ties NodeHeartbeater stale detection to PartitionStore
// claim/steal operations, and notifies the Router of the current owned set.
//
// Lifecycle:
//  1. Construct with NewPartitionManager (rtr may be nil initially).
//  2. Call SetRouter(rtr) to inject the Router before calling Run.
//  3. On startup: ClaimUnclaimed → call router.SetOwnedPartitions.
//  4. Every pollInterval: detect stale nodes → steal their partitions → call SetOwnedPartitions.
//  5. After g.Wait() in root.go: caller invokes ReleaseAll for graceful handoff.
type PartitionManager struct {
	store        *PartitionStore
	heartbeat    *NodeHeartbeater
	rtr          *router.Router
	nodeID       string
	pollInterval time.Duration

	mu    sync.RWMutex
	owned map[uint32]int64 // partitionID → epoch
}

// NewPartitionManager creates a PartitionManager. rtr may be nil when called
// before the Router is constructed; call SetRouter before Run.
// pollInterval defaults to 5s when zero.
func NewPartitionManager(store *PartitionStore, heartbeat *NodeHeartbeater, rtr *router.Router, pollInterval time.Duration) *PartitionManager {
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	return &PartitionManager{
		store:        store,
		heartbeat:    heartbeat,
		rtr:          rtr,
		nodeID:       heartbeat.NodeID(),
		pollInterval: pollInterval,
		owned:        make(map[uint32]int64),
	}
}

// SetRouter injects the Router after construction. Must be called before Run.
// This breaks the circular dependency between epochCursorStore wrapping (needs pm)
// and Router construction (needs wrapped cursorStore).
func (pm *PartitionManager) SetRouter(rtr *router.Router) {
	pm.rtr = rtr
}

// Run starts the partition management loop. It claims unclaimed partitions
// immediately, then polls on pollInterval to steal partitions from stale nodes.
// ReleaseAll is NOT called inside Run — root.go calls it explicitly after g.Wait()
// to guarantee cursor flush completes before ownership is released.
// Run is designed to be called in a goroutine (errgroup.Go).
func (pm *PartitionManager) Run(ctx context.Context) error {
	if err := pm.claimAndApply(ctx); err != nil {
		// Non-fatal: log and continue — another tick will retry.
		slog.Warn("cluster: initial partition claim failed", "err", err)
	}

	ticker := time.NewTicker(pm.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// ReleaseAll is called explicitly by root.go after g.Wait() drains
			// the Router and flushes cursors. Do not call here.
			return nil
		case <-ticker.C:
			pm.tick(ctx)
		}
	}
}

// OwnsPartition reports whether this node currently owns the given partitionID.
// Safe for concurrent use.
func (pm *PartitionManager) OwnsPartition(partitionID uint32) bool {
	pm.mu.RLock()
	_, ok := pm.owned[partitionID]
	pm.mu.RUnlock()
	return ok
}

// OwnedPartitions returns a sorted slice of partition IDs currently owned by
// this node. Always returns a non-nil slice.
func (pm *PartitionManager) OwnedPartitions() []uint32 {
	pm.mu.RLock()
	ids := make([]uint32, 0, len(pm.owned))
	for id := range pm.owned {
		ids = append(ids, id)
	}
	pm.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ReleaseAll releases all owned partitions in Postgres, clears the local owned
// map, and calls SetOwnedPartitions(nil) on the Router to restore full-partition
// behavior (or stops goroutines from starting for a stopped node).
// Called by root.go AFTER g.Wait() returns, so cursor flush in
// PostgresCursorStore.Run has already completed.
func (pm *PartitionManager) ReleaseAll(ctx context.Context) error {
	err := pm.store.ReleaseAll(ctx)
	pm.mu.Lock()
	pm.owned = make(map[uint32]int64)
	pm.mu.Unlock()
	pm.applyToRouter()
	if err != nil {
		slog.Warn("cluster: ReleaseAll failed", "err", err)
	} else {
		slog.Info("cluster: released all partitions on shutdown")
	}
	return err
}

// claimAndApply claims unclaimed partitions from Postgres and updates the
// Router's owned partition set. Called on startup and periodically by tick.
func (pm *PartitionManager) claimAndApply(ctx context.Context) error {
	claims, err := pm.store.ClaimUnclaimed(ctx)
	if err != nil {
		return err
	}
	if len(claims) > 0 {
		pm.mu.Lock()
		for _, c := range claims {
			pm.owned[c.PartitionID] = c.Epoch
		}
		pm.mu.Unlock()
		slog.Info("cluster: claimed partitions", "count", len(claims))
		pm.applyToRouter()
	}
	return nil
}

// stealPartitions steals all partitions owned by staleNodeID.
func (pm *PartitionManager) stealPartitions(ctx context.Context, staleNodeID string) {
	claims, err := pm.store.StealStalePartitions(ctx, staleNodeID)
	if err != nil {
		slog.Warn("cluster: steal partitions failed", "stale_node", staleNodeID, "err", err)
		return
	}
	if len(claims) == 0 {
		return
	}
	pm.mu.Lock()
	for _, c := range claims {
		pm.owned[c.PartitionID] = c.Epoch
	}
	pm.mu.Unlock()
	slog.Info("cluster: stole partitions from stale node", "stale_node", staleNodeID, "count", len(claims))
	pm.applyToRouter()
}

// applyToRouter calls rtr.SetOwnedPartitions with the current owned set.
// Must not be called under pm.mu (SetOwnedPartitions acquires Router.mu).
// No-op if rtr is nil (called before SetRouter).
func (pm *PartitionManager) applyToRouter() {
	if pm.rtr == nil {
		return
	}
	pm.rtr.SetOwnedPartitions(pm.OwnedPartitions())
}

// tick is the per-interval work: detect stale nodes and steal their partitions,
// then claim any newly unclaimed partitions.
func (pm *PartitionManager) tick(ctx context.Context) {
	staleThreshold := int(pm.pollInterval.Seconds() * 3) // 3× poll interval = stale
	stale, err := pm.heartbeat.StaleNodes(ctx, staleThreshold)
	if err != nil {
		slog.Warn("cluster: StaleNodes query failed", "err", err)
	} else {
		for _, staleNodeID := range stale {
			if staleNodeID == pm.nodeID {
				continue // never steal from ourselves
			}
			pm.stealPartitions(ctx, staleNodeID)
		}
	}
	// Also pick up any newly unclaimed partitions (handles join-after-crash race).
	if err := pm.claimAndApply(ctx); err != nil {
		slog.Warn("cluster: periodic claimAndApply failed", "err", err)
	}
}
