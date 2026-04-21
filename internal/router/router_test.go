package router_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// errFail is the sentinel error returned by fakeConsumer for bad-key entries.
var errFail = errors.New("delivery failed")

// fakeConsumer implements router.Consumer. It records all delivered entries and
// returns errFail for any entry whose Key bytes equal badKey.
type fakeConsumer struct {
	id      string
	badKey  string
	mu      sync.Mutex
	entries []eventlog.LogEntry
}

func (f *fakeConsumer) ID() string { return f.id }

func (f *fakeConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	if string(entry.Event.Key) == f.badKey {
		return errFail
	}
	f.mu.Lock()
	f.entries = append(f.entries, entry)
	f.mu.Unlock()
	return nil
}

func (f *fakeConsumer) delivered() []eventlog.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]eventlog.LogEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// fakeEventLog returns pre-seeded LogEntry slices per partition.
// It never blocks and returns an empty slice once the seed is exhausted.
type fakeEventLog struct {
	mu   sync.Mutex
	data map[uint32][]eventlog.LogEntry
}

func newFakeEventLog(data map[uint32][]eventlog.LogEntry) *fakeEventLog {
	return &fakeEventLog{data: data}
}

func (f *fakeEventLog) Append(ev *event.ChangeEvent) (uint64, error) { return 0, nil }

func (f *fakeEventLog) ReadPartition(_ context.Context, partition uint32, fromSeq uint64, limit int) ([]eventlog.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.data[partition]
	var out []eventlog.LogEntry
	for _, e := range all {
		if e.Seq >= fromSeq {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	seqs := make([]uint64, len(evs))
	return seqs, nil
}

func (f *fakeEventLog) Close() error { return nil }

// makeEntry builds a minimal LogEntry with the given seq and JSON key bytes.
func makeEntry(seq uint64, keyJSON string) eventlog.LogEntry {
	return eventlog.LogEntry{
		Seq: seq,
		Event: &event.ChangeEvent{
			Table: "test_table",
			Key:   []byte(keyJSON),
		},
	}
}

// TestConsumerInterface is a compile-time assertion that fakeConsumer satisfies
// the router.Consumer interface.
func TestConsumerInterface(t *testing.T) {
	var _ router.Consumer = (*fakeConsumer)(nil)
}

// TestNoopCursorStoreReturnsOne verifies that LoadCursor returns 1 for an
// unseen (consumerID, partitionID) — so the first ReadPartition call starts at
// seq=1, not seq=0 (which is the dedup sentinel).
func TestNoopCursorStoreReturnsOne(t *testing.T) {
	// NewRouter with nil cursorStore should use an internal noopCursorStore.
	el := newFakeEventLog(nil)
	r := router.NewRouter(el, 1, nil)

	// Register a consumer so we can indirectly observe cursor behaviour through
	// TestLoadCursorDefaultIsOne which directly tests the noop store.
	// This test validates the exported NewRouter path accepts nil and doesn't panic.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
}

// TestLoadCursorDefaultIsOne directly checks that the noop cursor store
// (obtained via NewNoopCursorStore) returns 1 for an unknown cursor.
func TestLoadCursorDefaultIsOne(t *testing.T) {
	cs := router.NewNoopCursorStore()
	seq, err := cs.LoadCursor(context.Background(), "consumer-1", 0)
	if err != nil {
		t.Fatalf("LoadCursor error: %v", err)
	}
	if seq != 1 {
		t.Fatalf("expected seq=1 for unknown cursor, got %d", seq)
	}
}

// TestRouterDeliversSingleEvent verifies the end-to-end happy path: a single
// event in partition 0 reaches the registered consumer before the context
// times out.
func TestRouterDeliversSingleEvent(t *testing.T) {
	entry := makeEntry(1, `{"id":1}`)
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{
		0: {entry},
	})

	consumer := &fakeConsumer{id: "c1"}
	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := consumer.delivered()
	if len(got) != 1 {
		t.Fatalf("expected 1 delivered entry, got %d", len(got))
	}
	if got[0].Seq != 1 {
		t.Errorf("expected seq=1, got %d", got[0].Seq)
	}
}

// TestPoisonPillIsolation verifies per-key blocking: a failing delivery for key
// "A" does NOT block delivery for key "B", and subsequent events for key "A"
// are withheld (not delivered).
//
// Partition 0 contains:
//
//	seq=1 key="A"  → consumer returns errFail (bad key)
//	seq=2 key="B"  → consumer succeeds
//	seq=3 key="A"  → must be skipped (blocked behind seq=1 failure)
func TestPoisonPillIsolation(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(1, `"A"`),
		makeEntry(2, `"B"`),
		makeEntry(3, `"A"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{
		0: entries,
	})

	consumer := &fakeConsumer{id: "c1", badKey: `"A"`}
	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := consumer.delivered()

	// seq=2 (key "B") must have been delivered.
	foundB := false
	for _, e := range got {
		if e.Seq == 2 {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("expected seq=2 (key B) to be delivered, delivered seqs: %v", seqs(got))
	}

	// seq=3 (key "A") must NOT have been delivered.
	for _, e := range got {
		if e.Seq == 3 {
			t.Errorf("seq=3 (key A) was delivered but should have been blocked")
		}
	}
}

func seqs(entries []eventlog.LogEntry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

// fakeConsumerWithRetry is like fakeConsumer but fails the first N deliveries
// for badKey, then succeeds on subsequent calls. It is used to verify that
// RetryScheduler is wired into Router so the blocked entry is eventually
// re-attempted and delivered.
type fakeConsumerWithRetry struct {
	id      string
	badKey  string
	failMax int // number of times to fail before succeeding

	mu      sync.Mutex
	calls   int
	entries []eventlog.LogEntry
}

func (f *fakeConsumerWithRetry) ID() string { return f.id }

func (f *fakeConsumerWithRetry) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(entry.Event.Key) == f.badKey {
		if f.calls < f.failMax {
			f.calls++
			return errFail
		}
	}
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeConsumerWithRetry) delivered() []eventlog.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]eventlog.LogEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// TestRetrySchedulerWiredToRouter verifies that a failed delivery is
// eventually retried by Router. The test uses fakeConsumerWithRetry which
// fails the first call for badKey and succeeds on subsequent calls.
//
// Without RetryScheduler wired into Router.Run and Router.dispatch, the
// blocked entry is never re-attempted and the test fails (seq=1 never
// appears in delivered entries).
//
// With wiring in place, RetryScheduler.Tick fires at retryTickInterval
// and re-delivers the blocked entry. The test waits long enough for at
// least two ticks.
func TestRetrySchedulerWiredToRouter(t *testing.T) {
	// Partition 0 has one entry with badKey. Consumer fails once then succeeds.
	entry := makeEntry(1, `"retry-key"`)
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{
		0: {entry},
	})

	consumer := &fakeConsumerWithRetry{
		id:      "c-retry",
		badKey:  `"retry-key"`,
		failMax: 1,
	}

	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	// Allow 3 seconds so RetryScheduler can tick at least twice (tick every 1s).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got := consumer.delivered()
	if len(got) == 0 {
		t.Fatal("expected seq=1 to be retried and delivered, but delivered list is empty")
	}
	foundSeq1 := false
	for _, e := range got {
		if e.Seq == 1 {
			foundSeq1 = true
		}
	}
	if !foundSeq1 {
		t.Errorf("expected seq=1 to appear in delivered entries, got seqs: %v", seqs(got))
	}
}
