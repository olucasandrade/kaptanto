package router_test

// Tests for RTR-06: Router must never persist a consumer's cursor past the
// lowest seq of a queued-but-undelivered RetryRecord for that
// consumer+partition. Before this fix, dispatchUpdateCursor persisted
// entry.Seq+1 unconditionally the moment a follow-on event for an
// already-blocked group was queued in RetryScheduler (memory-only) — a crash
// or restart while the group was blocked silently and permanently dropped
// every queued follow-on, with zero delivery attempts ever made.

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// cursorSave is one recorded SaveCursor call.
type cursorSave struct {
	consumerID  string
	partitionID uint32
	seq         uint64
}

// recordingCursorStore is a router.ConsumerCursorStore that behaves like the
// real (persistent) stores but also keeps a full history of every SaveCursor
// call, so tests can assert on values that were transiently persisted, not
// just the final value.
type recordingCursorStore struct {
	mu    sync.Mutex
	data  map[string]uint64
	calls []cursorSave
}

func newRecordingCursorStore() *recordingCursorStore {
	return &recordingCursorStore{data: make(map[string]uint64)}
}

func (r *recordingCursorStore) key(consumerID string, partitionID uint32) string {
	return consumerID + ":" + strconv.FormatUint(uint64(partitionID), 10)
}

func (r *recordingCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.key(consumerID, partitionID)] = seq
	r.calls = append(r.calls, cursorSave{consumerID: consumerID, partitionID: partitionID, seq: seq})
	return nil
}

func (r *recordingCursorStore) LoadCursor(_ context.Context, consumerID string, partitionID uint32) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.data[r.key(consumerID, partitionID)]
	if !ok {
		return 1, nil
	}
	return v, nil
}

// maxSeq returns the highest seq ever passed to SaveCursor for
// (consumerID, partitionID), across the entire call history — used to prove
// the persisted cursor never transiently exceeded a floor, even though the
// final value alone might look correct by coincidence.
func (r *recordingCursorStore) maxSeq(consumerID string, partitionID uint32) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var max uint64
	for _, c := range r.calls {
		if c.consumerID == consumerID && c.partitionID == partitionID && c.seq > max {
			max = c.seq
		}
	}
	return max
}

// TestPersistedCursorCappedAtBlockedFloor verifies the core RTR-06 invariant:
// while a message group is blocked, the persisted cursor for that
// consumer+partition never advances past the lowest seq of any record still
// queued (undelivered) in RetryScheduler, even as OTHER keys on the same
// partition keep being delivered and would otherwise push the cursor forward.
//
// Partition 0 contains: K1@1 (blocked forever in this test), K2@2, K2@3
// (both succeed), K1@4 (follow-on, queued behind the blocked K1@1 head).
func TestPersistedCursorCappedAtBlockedFloor(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(1, `"K1"`),
		makeEntry(2, `"K2"`),
		makeEntry(3, `"K2"`),
		makeEntry(4, `"K1"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})

	consumer := newControllableConsumer("floor-consumer")
	consumer.Block(`"K1"`)

	cs := newRecordingCursorStore()
	r := router.NewRouter(el, 1, cs)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	go r.Run(ctx) //nolint:errcheck

	// The single batch of 4 entries is processed almost immediately (no
	// notify channel on fakeEventLog, but ReadPartition doesn't block when
	// entries are available). 250ms is comfortably more than needed and well
	// under the 1s RetryScheduler tick, so K1 never gets retried mid-test.
	time.Sleep(250 * time.Millisecond)

	// K2@2 and K2@3 must each be delivered exactly once — proving the
	// in-memory cursor kept advancing past the blocked K1 group (otherwise
	// the read window would keep rewinding and re-delivering them every poll).
	got := consumer.delivered()
	counts := map[uint64]int{}
	for _, e := range got {
		counts[e.Seq]++
	}
	if counts[2] != 1 {
		t.Errorf("K2@2 expected delivered exactly once, got %d", counts[2])
	}
	if counts[3] != 1 {
		t.Errorf("K2@3 expected delivered exactly once, got %d", counts[3])
	}
	if counts[1] != 0 {
		t.Errorf("K1@1 must not be delivered while blocked, got %d deliveries", counts[1])
	}
	if counts[4] != 0 {
		t.Errorf("K1@4 (follow-on) must not be delivered while K1 stays blocked, got %d deliveries", counts[4])
	}

	// The floor for (floor-consumer, partition 0) is 1 (K1@1's seq) for the
	// entire test — it must never have been exceeded, even transiently, by
	// any SaveCursor call triggered by K2@2 or K2@3's successful delivery, or
	// by K1@4 being queued as a follow-on.
	if max := cs.maxSeq("floor-consumer", 0); max != 1 {
		t.Errorf("persisted cursor must never exceed the blocked floor (1) while K1 stays blocked; max persisted seq observed across all SaveCursor calls = %d", max)
	}
}

// TestCrashRestartRedeliversQueuedFollowOns is the crash-restart simulation
// from the fix plan's test plan: construct a NEW Router + fresh
// RetryScheduler over the SAME EventLog and cursor store (exactly what a
// process restart looks like), and assert that events which were only ever
// queued in the first Router's memory-only RetryScheduler — never actually
// delivered — are re-delivered in order after "restart".
//
// Partition 0 contains: K1@1 (blocked before crash), K2@2 (delivered before
// crash), K1@3 (follow-on, queued behind K1@1 before crash, never delivered).
//
// Before the RTR-06 fix this test fails: the pre-fix router persists
// entry.Seq+1 unconditionally, so after K1@3 is queued the persisted cursor
// is already 4 — past the end of the partition — and the "restarted" router
// never re-reads seq 1..3 at all, permanently losing K1@1 and K1@3.
func TestCrashRestartRedeliversQueuedFollowOns(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(1, `"K1"`),
		makeEntry(2, `"K2"`),
		makeEntry(3, `"K1"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	cs := newRecordingCursorStore()

	// --- Before "crash" ---
	// consumer1 blocks K1, so K1@1 and K1@3 only ever reach RetryScheduler's
	// in-memory retry queue; they are never actually delivered.
	consumer1 := newControllableConsumer("restart-consumer")
	consumer1.Block(`"K1"`)

	r1 := router.NewRouter(el, 1, cs)
	r1.Register(consumer1)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	go r1.Run(ctx1) //nolint:errcheck
	// Give it time to process the whole batch (K2@2 delivered, K1@1 blocked,
	// K1@3 queued as follow-on) but stay well under the 1s retry tick, so no
	// retry attempt — successful or not — happens before the simulated crash.
	time.Sleep(200 * time.Millisecond)
	cancel1()
	// r1 and its RetryScheduler (holding K1@1 and K1@3 only in memory) are
	// discarded here, unrecoverably, exactly like a process crash: nothing
	// ever drains them.

	preCrash := consumer1.delivered()
	if len(preCrash) != 1 || preCrash[0].Seq != 2 {
		t.Fatalf("pre-crash setup: expected only K2@2 delivered, got seqs %v", seqs(preCrash))
	}

	// --- "Restart" ---
	// Brand-new Router + brand-new RetryScheduler (NewRouter always builds
	// its own) over the SAME EventLog and cursor store. consumer2 is healthy
	// this time (a real deployment would fix or route around the failure
	// that caused the block before restarting).
	consumer2 := newControllableConsumer("restart-consumer")

	r2 := router.NewRouter(el, 1, cs)
	r2.Register(consumer2)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if err := r2.Run(ctx2); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := consumer2.delivered()
	var k1Seqs []uint64
	for _, e := range got {
		if e.Seq == 1 || e.Seq == 3 {
			k1Seqs = append(k1Seqs, e.Seq)
		}
	}
	if len(k1Seqs) != 2 {
		t.Fatalf("expected both K1@1 and K1@3 to be re-delivered after restart (a queued-but-undelivered follow-on must not be lost); delivered K1 seqs: %v, all delivered seqs: %v", k1Seqs, seqs(got))
	}
	if k1Seqs[0] != 1 || k1Seqs[1] != 3 {
		t.Errorf("expected K1@1 before K1@3 after restart (RTR-04 per-key ordering), got order: %v", k1Seqs)
	}
}

// TestDeadLetterReleasesFloorWhenQueueEmpties verifies that once a blocked
// group's sole queued record is dead-lettered (maxRetries exhausted), the
// RTR-06 floor for that consumer+partition is released (cleared) — otherwise
// a permanently-failing event would pin the persisted cursor forever, causing
// the whole partition window to re-deliver on every restart even though the
// bad event itself has already been given up on.
func TestDeadLetterReleasesFloorWhenQueueEmpties(t *testing.T) {
	rs := router.NewRetryScheduler()
	consumer := &alwaysFailConsumer{id: "dl-floor-empty"}

	entry := makeEntry(5, `"K1"`)
	entry.PartitionID = 2
	rs.AddBlocked(consumer, `"K1"`, &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  "dl-floor-empty",
	})

	if floor, ok := rs.Floor("dl-floor-empty", 2); !ok || floor != 5 {
		t.Fatalf("expected floor=5 immediately after AddBlocked, got floor=%d ok=%v", floor, ok)
	}

	type release struct {
		consumerID  string
		partitionID uint32
		floor       uint64
		ok          bool
	}
	var mu sync.Mutex
	var released []release
	rs.OnFloorReleased = func(consumerID string, partitionID uint32, floor uint64, ok bool) {
		mu.Lock()
		defer mu.Unlock()
		released = append(released, release{consumerID, partitionID, floor, ok})
	}

	const maxRetries = 15
	for i := 0; i < maxRetries; i++ {
		rs.ForceRetryNow(consumer, `"K1"`)
		rs.Tick(context.Background())
	}

	if rs.BlockedCount(consumer) != 0 {
		t.Fatalf("expected the group dead-lettered and cleared, got %d blocked groups", rs.BlockedCount(consumer))
	}
	if floor, ok := rs.Floor("dl-floor-empty", 2); ok {
		t.Errorf("expected the floor to be released (cleared) after dead-letter drained the queue, got floor=%d ok=%v", floor, ok)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(released) == 0 {
		t.Fatal("expected OnFloorReleased to fire when the dead-lettered head was popped")
	}
	last := released[len(released)-1]
	if last.ok {
		t.Errorf("expected the final OnFloorReleased call to report ok=false (no floor remains), got floor=%d ok=true", last.floor)
	}
	if last.consumerID != "dl-floor-empty" || last.partitionID != 2 {
		t.Errorf("OnFloorReleased called with wrong identity: consumer=%q partition=%d", last.consumerID, last.partitionID)
	}
}

// TestDeadLetterRaisesFloorWhenFollowOnRemains verifies that when a
// dead-lettered head has a follow-on behind it, the floor RISES to the
// follow-on's seq instead of clearing entirely — the follow-on is still
// queued and undelivered, so the persisted cursor still must not pass it.
func TestDeadLetterRaisesFloorWhenFollowOnRemains(t *testing.T) {
	rs := router.NewRetryScheduler()
	consumer := &alwaysFailConsumer{id: "dl-floor-followon"}

	head := makeEntry(5, `"K1"`)
	head.PartitionID = 2
	rs.AddBlocked(consumer, `"K1"`, &router.RetryRecord{
		Entry:       head,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  "dl-floor-followon",
	})

	followOn := makeEntry(9, `"K1"`)
	followOn.PartitionID = 2
	rs.AddBlocked(consumer, `"K1"`, &router.RetryRecord{
		Entry:       followOn,
		Attempts:    0,
		NextRetryAt: time.Now().Add(time.Hour), // not independently eligible yet
		ConsumerID:  "dl-floor-followon",
	})

	if floor, ok := rs.Floor("dl-floor-followon", 2); !ok || floor != 5 {
		t.Fatalf("expected floor=5 (the head), got floor=%d ok=%v", floor, ok)
	}

	const maxRetries = 15
	for i := 0; i < maxRetries; i++ {
		rs.ForceRetryNow(consumer, `"K1"`)
		rs.Tick(context.Background())
	}

	// The head (seq=5) is dead-lettered after maxRetries; the follow-on
	// (seq=9) becomes the new head and is made immediately eligible, but
	// `consumer` always fails, so it stays queued rather than delivered.
	if !rs.IsBlocked("dl-floor-followon", `"K1"`) {
		t.Fatal("expected the follow-on to still be blocked after the head dead-lettered")
	}
	floor, ok := rs.Floor("dl-floor-followon", 2)
	if !ok {
		t.Fatal("expected the floor to still apply — the follow-on remains queued and undelivered")
	}
	if floor != 9 {
		t.Errorf("expected the floor released (raised) to the follow-on's seq=9 after the head dead-lettered, got floor=%d", floor)
	}
}
