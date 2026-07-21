package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

type testConsumer struct {
	id string
}

func (c *testConsumer) ID() string { return c.id }
func (c *testConsumer) Deliver(context.Context, eventlog.LogEntry) error { return nil }

// fakeCursorStore records SaveCursor calls.
type fakeCursorStore struct {
	mu     sync.Mutex
	stored map[string]map[uint32]uint64
}

func newFakeCursorStore() *fakeCursorStore {
	return &fakeCursorStore{stored: make(map[string]map[uint32]uint64)}
}

func (f *fakeCursorStore) LoadCursor(_ context.Context, consumerID string, partitionID uint32) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.stored[consumerID]; ok {
		return m[partitionID], nil
	}
	return 0, nil
}

func (f *fakeCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stored[consumerID] == nil {
		f.stored[consumerID] = make(map[uint32]uint64)
	}
	f.stored[consumerID][partitionID] = seq
	return nil
}

func (f *fakeCursorStore) saved(consumerID string, partitionID uint32) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stored[consumerID][partitionID]
}

// TestSkippedPoisonPrecedenceOverBlockedGroup verifies that an entry which is
// both in the RTR-07 skip-set and behind a blocked message group advances the
// durable cursor instead of being re-queued as a blocked follow-on.
func TestSkippedPoisonPrecedenceOverBlockedGroup(t *testing.T) {
	c := &testConsumer{id: "c-skipped"}
	store := newFakeCursorStore()
	rs := NewRetryScheduler()
	r := &Router{
		consumers: []consumerState{{
			consumer:               c,
			cursorByPartition:      map[uint32]uint64{0: 5},
			provisionalByPartition: map[uint32]uint64{},
			skippedSeqs:            map[uint32]map[uint64]struct{}{0: {5: {}}},
		}},
		cursorStore: store,
		rs:          rs,
	}

	groupKey := "same-key"
	rs.AddBlocked(c, groupKey, &RetryRecord{
		Entry: eventlog.LogEntry{
			Seq:         6,
			PartitionID: 0,
			Event:       &event.ChangeEvent{Key: []byte(groupKey)},
		},
		Attempts:    1,
		NextRetryAt: time.Now(),
		ConsumerID:  c.ID(),
	})

	entry := eventlog.LogEntry{
		Seq:         5,
		PartitionID: 0,
		Event:       &event.ChangeEvent{Key: []byte(groupKey)},
	}

	r.dispatch(context.Background(), 0, entry, &[]consumerSnap{}, &[]error{})

	cs := &r.consumers[0]
	if cs.cursorByPartition[0] != 6 {
		t.Fatalf("cursor = %d, want 6", cs.cursorByPartition[0])
	}
	if got := store.saved(c.ID(), 0); got != 6 {
		t.Fatalf("persisted cursor = %d, want 6", got)
	}
	queue := rs.BlockedQueue(c.ID(), groupKey)
	if len(queue) != 1 || queue[0].Entry.Seq != 6 {
		t.Fatalf("blocked queue must keep the original follow-on only, got %+v", queue)
	}
}
