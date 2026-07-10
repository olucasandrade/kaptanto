package router

import (
	"context"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

type pruneFakeLog struct{}

func (pruneFakeLog) Append(*event.ChangeEvent) (uint64, error) { return 0, nil }
func (pruneFakeLog) AppendBatch([]*event.ChangeEvent) ([]uint64, error) {
	return nil, nil
}
func (pruneFakeLog) ReadPartition(context.Context, uint32, uint64, int) ([]eventlog.LogEntry, error) {
	return nil, nil
}
func (pruneFakeLog) Close() error { return nil }

type pruneBatchConsumer struct{ id string }

func (c *pruneBatchConsumer) ID() string { return c.id }
func (c *pruneBatchConsumer) Deliver(context.Context, eventlog.LogEntry) error { return nil }
func (c *pruneBatchConsumer) FlushBatch(context.Context, uint32) error         { return nil }

// TestPromoteProvisionalPrunesSkippedSeqs verifies RTR-07 prune: when the
// durable cursor advances past skipped seqs, they are removed from the
// memory-only skip-set.
func TestPromoteProvisionalPrunesSkippedSeqs(t *testing.T) {
	r := NewRouter(pruneFakeLog{}, 1, nil)
	r.Register(&pruneBatchConsumer{id: "prune"})

	r.mu.Lock()
	cs := &r.consumers[0]
	cs.skippedSeqs = map[uint32]map[uint64]struct{}{
		0: {1: {}, 3: {}, 10: {}},
	}
	cs.provisionalByPartition[0] = 5
	cs.cursorByPartition[0] = 1
	r.mu.Unlock()

	r.promoteProvisional(context.Background(), 0, 0)

	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.consumers[0].skippedSeqs[0]
	if _, ok := m[1]; ok {
		t.Fatal("seq 1 should be pruned (<= cursor 5)")
	}
	if _, ok := m[3]; ok {
		t.Fatal("seq 3 should be pruned (<= cursor 5)")
	}
	if _, ok := m[10]; !ok {
		t.Fatal("seq 10 should remain (> cursor 5)")
	}
	if r.consumers[0].cursorByPartition[0] != 5 {
		t.Fatalf("cursor = %d, want 5", r.consumers[0].cursorByPartition[0])
	}
}

// TestResetPoisonStreakClearsGuard verifies a successful flush path resets the
// consecutive-poison counter so further PermanentFlushError dead-letters are
// allowed again after the streak guard tripped.
func TestResetPoisonStreakClearsGuard(t *testing.T) {
	r := NewRouter(pruneFakeLog{}, 1, nil)
	r.Register(&pruneBatchConsumer{id: "streak-reset"})

	r.mu.Lock()
	cs := &r.consumers[0]
	cs.poisonStreak = map[uint32]int{0: poisonStreakLimit}
	cs.poisonStreakLogged = map[uint32]struct{}{0: {}}
	r.mu.Unlock()

	r.resetPoisonStreak(0, 0)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.consumers[0].poisonStreak[0]; ok {
		t.Fatal("poisonStreak should be cleared after reset")
	}
	if _, ok := r.consumers[0].poisonStreakLogged[0]; ok {
		t.Fatal("poisonStreakLogged should be cleared after reset")
	}
}
