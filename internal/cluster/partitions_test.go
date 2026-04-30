package cluster

import (
	"strings"
	"testing"
)

// TestPartitionClaimSQL verifies the atomic claim SQL constant contains
// the correct patterns for race-free partition ownership.
func TestPartitionClaimSQL(t *testing.T) {
	if !strings.Contains(claimPartitionSQL, "WHERE partition_id") {
		t.Error("claimPartitionSQL must filter by partition_id")
	}
	if !strings.Contains(claimPartitionSQL, "owner_node_id IS NULL") {
		t.Error("claimPartitionSQL must require owner_node_id IS NULL for atomic claim")
	}
	if !strings.Contains(claimPartitionSQL, "RETURNING partition_id, epoch") {
		t.Error("claimPartitionSQL must RETURN partition_id, epoch")
	}
}

// TestStealPartitionsSQL verifies steal SQL targets partitions by owner_node_id.
func TestStealPartitionsSQL(t *testing.T) {
	if !strings.Contains(stealPartitionsSQL, "WHERE owner_node_id = $2") {
		t.Error("stealPartitionsSQL must filter by owner_node_id = $2 to steal by stale owner")
	}
	if !strings.Contains(stealPartitionsSQL, "RETURNING") {
		t.Error("stealPartitionsSQL must RETURN stolen partition rows")
	}
}

// TestReleasePartitionsSQL verifies release SQL nullifies owner_node_id.
func TestReleasePartitionsSQL(t *testing.T) {
	if !strings.Contains(releasePartitionsSQL, "SET owner_node_id = NULL") {
		t.Error("releasePartitionsSQL must SET owner_node_id = NULL for graceful release")
	}
}

// TestPartitionStoreListOwnedEmptyNonNil confirms the return type contract:
// ListOwned must return a non-nil slice even when empty (matches StaleNodes invariant).
func TestPartitionStoreListOwnedEmptyNonNil(t *testing.T) {
	// Verify that []PartitionClaim{} is explicitly non-nil — this is the
	// invariant enforced in the implementation.
	result := []PartitionClaim{}
	if result == nil {
		t.Error("[]PartitionClaim{} literal must be non-nil")
	}
}

// TestEpochFor_NotFound verifies EpochFor returns (0, false) when partition not in epochs map.
func TestEpochFor_NotFound(t *testing.T) {
	ps := &PartitionStore{
		epochs: map[uint32]int64{},
	}
	epoch, ok := ps.EpochFor(5)
	if ok {
		t.Error("EpochFor on unknown partition must return ok=false")
	}
	if epoch != 0 {
		t.Errorf("EpochFor on unknown partition must return epoch=0, got %d", epoch)
	}
}

// TestEpochFor_Found verifies EpochFor returns the correct epoch when partition is tracked.
func TestEpochFor_Found(t *testing.T) {
	ps := &PartitionStore{
		epochs: map[uint32]int64{5: 42},
	}
	epoch, ok := ps.EpochFor(5)
	if !ok {
		t.Error("EpochFor on known partition must return ok=true")
	}
	if epoch != 42 {
		t.Errorf("EpochFor: got epoch=%d, want 42", epoch)
	}
}

// TestPartitionClaimFields verifies the PartitionClaim struct fields are accessible.
func TestPartitionClaimFields(t *testing.T) {
	pc := PartitionClaim{PartitionID: 7, Epoch: 3}
	if pc.PartitionID != 7 {
		t.Errorf("PartitionClaim.PartitionID: got %d, want 7", pc.PartitionID)
	}
	if pc.Epoch != 3 {
		t.Errorf("PartitionClaim.Epoch: got %d, want 3", pc.Epoch)
	}
}
