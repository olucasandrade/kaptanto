package router_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// errFlush is the sentinel error returned by fakeCursorConsumer.FlushBatch
// when simulating a broker outage.
var errFlush = errors.New("flush failed: broker unreachable")

// fakeCursorConsumer implements router.Consumer and router.BatchFlusher. It
// mimics the pop-before-send pattern used by all five real broker sinks
// (kafka, nats, sqs, pubsub, rabbitmq): Deliver only buffers the seq into an
// in-memory pending map; FlushBatch pops the batch out of pending and either
// records it as durably "flushed" (broker acked) or drops it and returns an
// error (broker rejected/unreachable) — exactly like the real sinks, which
// never put a failed batch back into pending. Whether the dropped batch is
// ever seen again depends entirely on the Router re-delivering the same
// window, which is the behavior under test.
type fakeCursorConsumer struct {
	id string

	failFlush atomic.Bool

	mu            sync.Mutex
	pending       map[uint32][]uint64
	flushed       []uint64
	flushAttempts []time.Time
}

var _ router.Consumer = (*fakeCursorConsumer)(nil)
var _ router.BatchFlusher = (*fakeCursorConsumer)(nil)

func (f *fakeCursorConsumer) ID() string { return f.id }

func (f *fakeCursorConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	f.mu.Lock()
	if f.pending == nil {
		f.pending = make(map[uint32][]uint64)
	}
	f.pending[entry.PartitionID] = append(f.pending[entry.PartitionID], entry.Seq)
	f.mu.Unlock()
	return nil
}

func (f *fakeCursorConsumer) FlushBatch(_ context.Context, partitionID uint32) error {
	f.mu.Lock()
	f.flushAttempts = append(f.flushAttempts, time.Now())
	batch := f.pending[partitionID]
	delete(f.pending, partitionID)
	f.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	if f.failFlush.Load() {
		// Mirror real sinks: the popped batch is dropped on failure, never
		// re-queued internally. Only the Router re-reading and re-delivering
		// the same window can recover it.
		return errFlush
	}

	f.mu.Lock()
	f.flushed = append(f.flushed, batch...)
	f.mu.Unlock()
	return nil
}

func (f *fakeCursorConsumer) getFlushed() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint64, len(f.flushed))
	copy(out, f.flushed)
	return out
}

func (f *fakeCursorConsumer) getFlushAttempts() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.flushAttempts))
	copy(out, f.flushAttempts)
	return out
}

// recordingCursorStore (and its constructor, newRecordingCursorStore) is
// defined once for the package in cursor_floor_test.go; this file's tests use
// its lastSaved method.

// TestBatchFlusherCursorDeferredUntilFlushSucceeds is the core regression test
// for the queue-sink-flushbatch-loss fix. Before the fix, dispatch Phase 3
// advanced and persisted the consumer cursor as soon as Deliver returned nil
// — i.e. as soon as the event was buffered, not sent. A FlushBatch failure
// then silently dropped the batch (see fakeCursorConsumer.FlushBatch, which
// mirrors every real sink's pop-before-send behavior) with the cursor already
// past it: permanent event loss.
//
// This test seeds 3 events on one partition behind a permanently-failing
// FlushBatch, asserts nothing is durably flushed and the cursor store is
// never advanced while the "broker" is down, then recovers the "broker" and
// asserts all 3 events are (re-)delivered and flushed exactly once, and the
// cursor store ends up at seq=4 (past all 3) — never further.
func TestBatchFlusherCursorDeferredUntilFlushSucceeds(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(1, `"k1"`),
		makeEntry(2, `"k2"`),
		makeEntry(3, `"k3"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})

	consumer := &fakeCursorConsumer{id: "kafka-defer"}
	consumer.failFlush.Store(true)

	cs := newRecordingCursorStore()
	r := router.NewRouter(el, 1, cs)
	r.Register(consumer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()

	// Wait until the router has attempted (and failed) at least one flush,
	// rather than sleeping a fixed duration — a slow runner could otherwise
	// let this pass without ever exercising the failure/discard path.
	attemptDeadline := time.After(2 * time.Second)
waitForAttempt:
	for {
		select {
		case <-attemptDeadline:
			cancel()
			<-done
			t.Fatal("router never attempted FlushBatch while broker was down")
		default:
		}
		if len(consumer.getFlushAttempts()) > 0 {
			break waitForAttempt
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := consumer.getFlushed(); len(got) != 0 {
		cancel()
		<-done
		t.Fatalf("expected zero successfully-flushed seqs while broker is down, got %v", got)
	}
	if seq, ok := cs.lastSaved(consumer.ID(), 0); ok {
		cancel()
		<-done
		t.Fatalf("cursor store must never contain a seq past an unacked record — "+
			"expected no SaveCursor call while FlushBatch keeps failing (CHK-01 violation), got saved seq=%d", seq)
	}

	// Recover the "broker" well before the NextDelay(0)==1s backoff elapses,
	// so the next scheduled retry succeeds.
	consumer.failFlush.Store(false)

	deadline := time.After(3 * time.Second)
waitLoop:
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("flush never succeeded after broker recovery within 3s")
		default:
		}
		if len(consumer.getFlushed()) >= 3 {
			break waitLoop
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	flushed := consumer.getFlushed()
	counts := map[uint64]int{}
	for _, seq := range flushed {
		counts[seq]++
	}
	for _, want := range []uint64{1, 2, 3} {
		if counts[want] != 1 {
			t.Errorf("seq=%d flushed %d times, want exactly 1 (router must stop re-delivering once flush succeeds)", want, counts[want])
		}
	}

	seq, ok := cs.lastSaved(consumer.ID(), 0)
	if !ok {
		t.Fatal("expected cursor to be saved once FlushBatch succeeded")
	}
	if seq != 4 {
		t.Errorf("expected persisted cursor seq=4 (past all 3 flushed entries, never further), got %d", seq)
	}
}

// TestMixedConsumersSSEUnaffectedByBatchFlusherFailure covers the plan's Risk
// (a): re-reading a window after a failed FlushBatch re-delivers it to every
// consumer registered on the partition, not just the failing one, because
// ReadPartition's fromSeq is the MINIMUM cursor across all consumers
// (minCursorForPartition). This test proves that re-delivery is harmless to a
// healthy, non-batching consumer sharing the partition: its own
// entry.Seq < snap.cursor guard (dispatch, Phase 2) filters out anything it
// already processed, so it must receive each event exactly once even while a
// sibling BatchFlusher consumer's broker is permanently down and its cursor
// never advances.
func TestMixedConsumersSSEUnaffectedByBatchFlusherFailure(t *testing.T) {
	entries := []eventlog.LogEntry{
		makeEntry(1, `"m1"`),
		makeEntry(2, `"m2"`),
		makeEntry(3, `"m3"`),
		makeEntry(4, `"m4"`),
		makeEntry(5, `"m5"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})

	sse := &fakeConsumer{id: "sse-mixed"}
	kafka := &fakeCursorConsumer{id: "kafka-mixed"}
	kafka.failFlush.Store(true) // broker permanently down for the whole test

	r := router.NewRouter(el, 1, nil)
	r.Register(sse)
	r.Register(kafka)

	// Long enough to span at least two read/dispatch cycles for the stuck
	// partition: the first attempt fails immediately, the second after the
	// NextDelay(0)==1s backoff.
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	counts := map[uint64]int{}
	for _, e := range sse.delivered() {
		counts[e.Seq]++
	}
	for seq := uint64(1); seq <= 5; seq++ {
		if counts[seq] != 1 {
			t.Errorf("sse consumer: seq=%d delivered %d times, want exactly 1 "+
				"(kafka consumer's stuck cursor re-widens the read window, but sse's own cursor must gate re-delivery)", seq, counts[seq])
		}
	}

	if got := kafka.getFlushed(); len(got) != 0 {
		t.Errorf("kafka consumer should never have flushed successfully (broker permanently down for this test), got %v", got)
	}
}

// TestBatchFlusherBackoffAvoidsHotLoop covers plan step 4: consecutive
// FlushBatch failures for a partition must be throttled by the
// RetryScheduler's NextDelay backoff schedule, not retried on every poll
// iteration. Without backoff, a down broker would cause runPartition to spin
// ReadPartition/dispatch/FlushBatch continuously, producing hundreds of
// attempts within the test window; with backoff, NextDelay(0) == 1s bounds
// the loop to roughly one attempt per second while the failure persists.
func TestBatchFlusherBackoffAvoidsHotLoop(t *testing.T) {
	entries := []eventlog.LogEntry{makeEntry(1, `"backoff-key"`)}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})

	consumer := &fakeCursorConsumer{id: "kafka-backoff"}
	consumer.failFlush.Store(true) // broker permanently down for this test

	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	attempts := consumer.getFlushAttempts()
	if len(attempts) < 2 {
		t.Fatalf("expected at least 2 FlushBatch attempts (initial failure + 1 backoff-scheduled retry) within 1.3s, got %d", len(attempts))
	}
	// A hot loop (no backoff) would produce hundreds of attempts in this
	// window; NextDelay(0)==1s bounds a healthy backoff to ~2-3 attempts.
	if len(attempts) > 5 {
		t.Fatalf("too many FlushBatch attempts (%d) within 1.3s — backoff was not applied, router is hot-looping", len(attempts))
	}
	gap := attempts[1].Sub(attempts[0])
	if gap < 800*time.Millisecond {
		t.Errorf("expected roughly a 1s gap between the first and second FlushBatch attempt (NextDelay(0)==1s backoff), got %v", gap)
	}
}
