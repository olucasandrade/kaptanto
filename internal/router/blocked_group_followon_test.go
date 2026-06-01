package router_test

// Tests for the blocked-group follow-on fix (RTR-04).
//
// Before the fix, subsequent events for an already-blocked message group were
// silently dropped instead of being enqueued behind the blocked head. These
// tests verify that follow-on entries are queued and delivered in order once
// the head entry eventually succeeds.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// controllableConsumer delivers entries normally but fails for a given key
// while a per-key "blocked" flag is set. Callers toggle the flag to simulate
// transient consumer failures.
type controllableConsumer struct {
	id string

	mu      sync.Mutex
	blocked map[string]*atomic.Bool // key → should fail?
	entries []eventlog.LogEntry
}

func newControllableConsumer(id string) *controllableConsumer {
	return &controllableConsumer{
		id:      id,
		blocked: make(map[string]*atomic.Bool),
	}
}

func (c *controllableConsumer) ID() string { return c.id }

// Block makes Deliver return errFail for the given key.
func (c *controllableConsumer) Block(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flag, ok := c.blocked[key]
	if !ok {
		flag = &atomic.Bool{}
		c.blocked[key] = flag
	}
	flag.Store(true)
}

// Unblock makes Deliver succeed for the given key.
func (c *controllableConsumer) Unblock(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if flag, ok := c.blocked[key]; ok {
		flag.Store(false)
	}
}

func (c *controllableConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	key := string(entry.Event.Key)
	c.mu.Lock()
	flag := c.blocked[key]
	c.mu.Unlock()
	if flag != nil && flag.Load() {
		return errFail
	}
	c.mu.Lock()
	c.entries = append(c.entries, entry)
	c.mu.Unlock()
	return nil
}

func (c *controllableConsumer) delivered() []eventlog.LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]eventlog.LogEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

// TestFollowOnEntryQueuedWhenGroupBlocked verifies that events for a blocked
// key that arrive after the initial failure are preserved (not dropped) and
// delivered in order once the consumer recovers.
//
// Partition 0 contains: K1@5 (fails), K2@6, K2@7, K1@8
//
// Expected outcome after K1 unblocks and retries drain:
//   - K1@5 and K1@8 are both delivered, K1@5 before K1@8.
//   - K2@6 and K2@7 are each delivered exactly once.
func TestFollowOnEntryQueuedWhenGroupBlocked(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(5, `"K1"`),
		makeEntry(6, `"K2"`),
		makeEntry(7, `"K2"`),
		makeEntry(8, `"K1"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})

	consumer := newControllableConsumer("ctrl")
	consumer.Block(`"K1"`)

	// We drive the test via the router: once we've given it enough time to read
	// all entries and enqueue K1@5 and K1@8, we unblock K1 and wait for the
	// RetryScheduler ticker to fire (fires every 1s).
	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	go r.Run(ctx) //nolint:errcheck

	// Give the partition loop enough time to read and attempt delivery of all
	// four entries. After this, K1@5 and K1@8 should be in the retry queue.
	time.Sleep(150 * time.Millisecond)

	// K2@6 and K2@7 must already have been delivered (different key, no block).
	got := consumer.delivered()
	seqSet := map[uint64]int{}
	for _, e := range got {
		seqSet[e.Seq]++
	}
	if seqSet[6] != 1 {
		t.Errorf("K2@6 expected delivered once before unblock, got %d", seqSet[6])
	}
	if seqSet[7] != 1 {
		t.Errorf("K2@7 expected delivered once before unblock, got %d", seqSet[7])
	}
	// K1 entries must NOT have been delivered yet.
	if seqSet[5] != 0 {
		t.Errorf("K1@5 must not be delivered while blocked, got %d", seqSet[5])
	}
	if seqSet[8] != 0 {
		t.Errorf("K1@8 must not be delivered while blocked, got %d", seqSet[8])
	}

	// Unblock K1 and wait for the RetryScheduler ticker to fire (fires every 1s).
	consumer.Unblock(`"K1"`)

	// Wait for K1@5 and K1@8 to be retried and delivered (up to 3 seconds).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got = consumer.delivered()
		seqMap := map[uint64]int{}
		for _, e := range got {
			seqMap[e.Seq]++
		}
		if seqMap[5] >= 1 && seqMap[8] >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	finalGot := consumer.delivered()
	finalSeqMap := map[uint64]int{}
	var finalSeqs []uint64
	for _, e := range finalGot {
		finalSeqMap[e.Seq]++
		finalSeqs = append(finalSeqs, e.Seq)
	}

	if finalSeqMap[5] < 1 {
		t.Errorf("K1@5 was never delivered after unblock; delivered seqs: %v", finalSeqs)
	}
	if finalSeqMap[8] < 1 {
		t.Errorf("K1@8 was never delivered after unblock (follow-on was dropped); delivered seqs: %v", finalSeqs)
	}
	if finalSeqMap[6] > 1 {
		t.Errorf("K2@6 delivered more than once (%d times); seqs: %v", finalSeqMap[6], finalSeqs)
	}
	if finalSeqMap[7] > 1 {
		t.Errorf("K2@7 delivered more than once (%d times); seqs: %v", finalSeqMap[7], finalSeqs)
	}

	// Verify ordering: K1@5 must appear before K1@8 in the delivery slice.
	var k1Positions []int
	for i, e := range finalGot {
		if e.Seq == 5 || e.Seq == 8 {
			k1Positions = append(k1Positions, int(e.Seq)*1000+i)
		}
	}
	if len(k1Positions) >= 2 {
		first := k1Positions[0] / 1000
		second := k1Positions[1] / 1000
		if first != 5 || second != 8 {
			t.Errorf("K1 ordering violated: expected K1@5 before K1@8, got seqs %d then %d", first, second)
		}
	}

	cancel()
}

// TestRetrySchedulerFollowOnQueue tests the RetryScheduler directly:
// - AddBlocked twice for the same groupKey queues records in order.
// - Tick retries the head first; follow-on becomes head after success.
func TestRetrySchedulerFollowOnQueue(t *testing.T) {
	rs := router.NewRetryScheduler()

	consumer := &fakeConsumer{id: "q-consumer", badKey: `"K1"`}

	// Simulate initial failure for K1@1.
	head := &router.RetryRecord{
		Entry:       makeEntry(1, `"K1"`),
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second), // immediately eligible
		ConsumerID:  "q-consumer",
	}
	rs.AddBlocked(consumer, `"K1"`, head)

	// Simulate follow-on event K1@3 arriving while K1 is still blocked.
	followOn := &router.RetryRecord{
		Entry:       makeEntry(3, `"K1"`),
		Attempts:    0,
		NextRetryAt: time.Now().Add(time.Hour), // not yet eligible on its own
		ConsumerID:  "q-consumer",
	}
	rs.AddBlocked(consumer, `"K1"`, followOn)

	// Group should still be blocked (queue length 2).
	if !rs.IsBlocked("q-consumer", `"K1"`) {
		t.Fatal("expected group to be blocked after two AddBlocked calls")
	}

	// Tick while K1 delivery still fails: head stays, attempts increment.
	rs.Tick(context.Background())
	if !rs.IsBlocked("q-consumer", `"K1"`) {
		t.Fatal("expected group still blocked after failed tick")
	}

	// Now unblock K1 so delivery succeeds.
	consumer.badKey = ""
	// Force the head record to be immediately eligible for retry.
	rs.ForceRetryNow(consumer, `"K1"`)

	// First successful Tick should deliver the head (seq=1) and promote the
	// follow-on (seq=3) as new head.
	rs.Tick(context.Background())

	// Group should still be blocked (follow-on still pending).
	if !rs.IsBlocked("q-consumer", `"K1"`) {
		t.Fatal("expected group to remain blocked after head delivered (follow-on still pending)")
	}

	// Verify seq=1 was delivered and seq=3 not yet.
	got := consumer.delivered()
	seqMap := map[uint64]int{}
	for _, e := range got {
		seqMap[e.Seq]++
	}
	if seqMap[1] != 1 {
		t.Errorf("expected seq=1 delivered once after first successful tick, got %d", seqMap[1])
	}
	if seqMap[3] != 0 {
		t.Errorf("seq=3 (follow-on) must not be delivered yet, got %d", seqMap[3])
	}

	// Second successful tick should deliver seq=3 and clear the group.
	rs.Tick(context.Background())

	if rs.IsBlocked("q-consumer", `"K1"`) {
		t.Fatal("expected group to be unblocked after all entries delivered")
	}

	got = consumer.delivered()
	seqMap = map[uint64]int{}
	for _, e := range got {
		seqMap[e.Seq]++
	}
	if seqMap[3] != 1 {
		t.Errorf("expected seq=3 delivered once after second successful tick, got %d", seqMap[3])
	}

	// Ordering: seq=1 must appear before seq=3.
	got = consumer.delivered()
	idx1, idx3 := -1, -1
	for i, e := range got {
		if e.Seq == 1 {
			idx1 = i
		}
		if e.Seq == 3 {
			idx3 = i
		}
	}
	if idx1 < 0 || idx3 < 0 || idx1 >= idx3 {
		t.Errorf("ordering violated: seq=1 at index %d, seq=3 at index %d (expected idx1 < idx3)", idx1, idx3)
	}
}
