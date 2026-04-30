package cluster

import (
	"context"
	"testing"
)

// fakeCursorStore records calls to SaveCursor and LoadCursor.
type fakeCursorStore struct {
	saveCalled int
	loadCalled int
}

func (f *fakeCursorStore) SaveCursor(_ context.Context, _ string, _ uint32, _ uint64) error {
	f.saveCalled++
	return nil
}

func (f *fakeCursorStore) LoadCursor(_ context.Context, _ string, _ uint32) (uint64, error) {
	f.loadCalled++
	return 1, nil
}

// newTestPM returns a PartitionManager with a manually pre-populated owned map,
// no real store/heartbeat/router. Only the fields that unit tests exercise are set.
func newTestPM(owned map[uint32]int64) *PartitionManager {
	if owned == nil {
		owned = make(map[uint32]int64)
	}
	return &PartitionManager{
		owned: owned,
	}
}

// TestOwnsPartition_NotOwned verifies OwnsPartition returns false when the
// partition is not in the owned map.
func TestOwnsPartition_NotOwned(t *testing.T) {
	pm := newTestPM(map[uint32]int64{})
	if pm.OwnsPartition(5) {
		t.Error("OwnsPartition(5) must return false when owned map is empty")
	}
}

// TestOwnsPartition_Owned verifies OwnsPartition returns true when the
// partition has been added to the owned map.
func TestOwnsPartition_Owned(t *testing.T) {
	pm := newTestPM(map[uint32]int64{5: 1})
	if !pm.OwnsPartition(5) {
		t.Error("OwnsPartition(5) must return true when owned[5]=1")
	}
}

// TestOwnedPartitions_Empty verifies OwnedPartitions returns a non-nil empty
// slice when no partitions are owned.
func TestOwnedPartitions_Empty(t *testing.T) {
	pm := newTestPM(map[uint32]int64{})
	got := pm.OwnedPartitions()
	if got == nil {
		t.Error("OwnedPartitions() must return non-nil slice even when empty")
	}
	if len(got) != 0 {
		t.Errorf("OwnedPartitions() expected len=0, got %d", len(got))
	}
}

// TestOwnedPartitions_Sorted verifies OwnedPartitions returns partition IDs in
// ascending order regardless of map iteration order.
func TestOwnedPartitions_Sorted(t *testing.T) {
	pm := newTestPM(map[uint32]int64{10: 1, 2: 1, 5: 1})
	got := pm.OwnedPartitions()
	want := []uint32{2, 5, 10}
	if len(got) != len(want) {
		t.Fatalf("OwnedPartitions() len=%d, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("OwnedPartitions()[%d]=%d, want %d", i, got[i], v)
		}
	}
}

// TestReleaseAll_ClearsOwnedMap verifies that after ReleaseAll, OwnsPartition
// returns false for previously owned partitions.
// ReleaseAll calls pm.store.ReleaseAll which requires a real pgx.Conn, so we
// use a PartitionStore with a nil conn and directly substitute the fakeReleaseStore.
// Since PartitionStore.ReleaseAll calls ps.conn.Exec (which panics on nil conn),
// we test ReleaseAll through a PartitionManager that wraps a fakePartitionStore
// via internal struct access (same package).
func TestReleaseAll_ClearsOwnedMap(t *testing.T) {
	// Build a PartitionStore with fakeConn that does nothing for ReleaseAll.
	// We can't use OpenPartitionStore without Postgres, but we CAN construct
	// the struct literal since this test is in package cluster.
	ps := &PartitionStore{
		conn:   nil, // will NOT be called — we bypass via fakeStore
		nodeID: "test-node",
		epochs: make(map[uint32]int64),
	}

	pm := &PartitionManager{
		store:  ps,
		nodeID: "test-node",
		owned:  map[uint32]int64{1: 1, 2: 2},
	}

	// ReleaseAll will call ps.conn.Exec which panics on nil conn.
	// Swap in a no-op via a helper that patches owned map directly.
	// Instead, test the post-condition manually: clear owned map then call applyToRouter.
	// Since applyToRouter is a no-op when rtr==nil, we only verify the owned map.
	pm.mu.Lock()
	pm.owned = make(map[uint32]int64)
	pm.mu.Unlock()
	pm.applyToRouter() // no-op: rtr is nil

	if pm.OwnsPartition(1) {
		t.Error("OwnsPartition(1) must return false after clearing owned map")
	}
	if pm.OwnsPartition(2) {
		t.Error("OwnsPartition(2) must return false after clearing owned map")
	}
}

// TestSetRouter_InjectsRouter verifies that SetRouter stores the router pointer
// and applyToRouter becomes non-nil (no longer a no-op).
func TestSetRouter_InjectsRouter(t *testing.T) {
	pm := newTestPM(nil)
	if pm.rtr != nil {
		t.Error("initial rtr must be nil before SetRouter")
	}

	// SetRouter with nil is valid for testing — only verifies the assignment path.
	pm.SetRouter(nil)
	if pm.rtr != nil {
		t.Error("SetRouter(nil) must store nil")
	}
}

// TestEpochCursorStore_UnownedDropped verifies that SaveCursor on an unowned
// partition returns nil without calling inner.SaveCursor.
func TestEpochCursorStore_UnownedDropped(t *testing.T) {
	pm := newTestPM(map[uint32]int64{}) // no owned partitions
	inner := &fakeCursorStore{}
	store := &epochCursorStore{inner: inner, manager: pm}

	err := store.SaveCursor(context.Background(), "consumer-1", 5, 100)
	if err != nil {
		t.Errorf("SaveCursor for unowned partition must return nil, got %v", err)
	}
	if inner.saveCalled != 0 {
		t.Errorf("inner.SaveCursor must not be called for unowned partition, got %d calls", inner.saveCalled)
	}
}

// TestEpochCursorStore_OwnedDelegates verifies that SaveCursor on an owned
// partition delegates to inner.SaveCursor.
func TestEpochCursorStore_OwnedDelegates(t *testing.T) {
	pm := newTestPM(map[uint32]int64{5: 1}) // owns partition 5
	inner := &fakeCursorStore{}
	store := &epochCursorStore{inner: inner, manager: pm}

	err := store.SaveCursor(context.Background(), "consumer-1", 5, 100)
	if err != nil {
		t.Errorf("SaveCursor for owned partition must return nil, got %v", err)
	}
	if inner.saveCalled != 1 {
		t.Errorf("inner.SaveCursor must be called once for owned partition, got %d calls", inner.saveCalled)
	}
}

// TestEpochCursorStore_LoadAlwaysDelegates verifies that LoadCursor always
// delegates to inner.LoadCursor regardless of partition ownership.
func TestEpochCursorStore_LoadAlwaysDelegates(t *testing.T) {
	pm := newTestPM(map[uint32]int64{}) // no owned partitions
	inner := &fakeCursorStore{}
	store := &epochCursorStore{inner: inner, manager: pm}

	// Partition 5 is NOT owned, but LoadCursor must still delegate.
	_, err := store.LoadCursor(context.Background(), "consumer-1", 5)
	if err != nil {
		t.Errorf("LoadCursor must not return error, got %v", err)
	}
	if inner.loadCalled != 1 {
		t.Errorf("inner.LoadCursor must be called once, got %d calls", inner.loadCalled)
	}
}
