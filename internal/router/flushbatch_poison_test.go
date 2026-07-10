package router_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type poisonBatchConsumer struct {
	id string

	mu         sync.Mutex
	poisonSeqs map[uint64]bool
	pending    map[uint32][]uint64
	delivered  []uint64
	flushed    []uint64
	flushN     int
}

func newPoisonBatchConsumer(id string, poison ...uint64) *poisonBatchConsumer {
	m := make(map[uint64]bool, len(poison))
	for _, s := range poison {
		m[s] = true
	}
	return &poisonBatchConsumer{
		id:         id,
		poisonSeqs: m,
		pending:    make(map[uint32][]uint64),
	}
}

func (c *poisonBatchConsumer) ID() string { return c.id }

func (c *poisonBatchConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delivered = append(c.delivered, entry.Seq)
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], entry.Seq)
	return nil
}

func (c *poisonBatchConsumer) FlushBatch(_ context.Context, partitionID uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushN++
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	if len(batch) == 0 {
		return nil
	}
	for _, seq := range batch {
		if c.poisonSeqs[seq] {
			return &router.PermanentFlushError{Seq: seq, Cause: errors.New("poison")}
		}
	}
	c.flushed = append(c.flushed, batch...)
	return nil
}

func (c *poisonBatchConsumer) getFlushed() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]uint64, len(c.flushed))
	copy(out, c.flushed)
	return out
}

func (c *poisonBatchConsumer) getDelivered() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]uint64, len(c.delivered))
	copy(out, c.delivered)
	return out
}

func poisonEntry(seq uint64, keyJSON string) eventlog.LogEntry {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	return eventlog.LogEntry{
		Seq:         seq,
		PartitionID: 0,
		Raw:         []byte(`{"ok":true}`),
		Event: &event.ChangeEvent{
			ID:             id,
			Table:          "orders",
			Key:            []byte(keyJSON),
			IdempotencyKey: "pg:public.orders:poison:" + keyJSON,
		},
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func countSeq(seqs []uint64, want uint64) int {
	n := 0
	for _, s := range seqs {
		if s == want {
			n++
		}
	}
	return n
}

// syncBuf is a bytes.Buffer with a mutex so slog handlers and test readers
// do not race under -race.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) contains(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.b.Bytes(), []byte(sub))
}

func TestFlushBatchPoisonSkipSetAndRedelivery(t *testing.T) {
	entries := []eventlog.LogEntry{
		poisonEntry(1, `"k1"`),
		poisonEntry(2, `"k2"`),
		poisonEntry(3, `"k3"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	store := &fakeDLQStore{}
	consumer := newPoisonBatchConsumer("poison-skip", 1)
	cs := newRecordingCursorStore()

	r := router.NewRouter(el, 1, cs)
	r.SetDLQ(store)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	waitUntil(t, 1500*time.Millisecond, func() bool {
		return store.writeCount() >= 1 && len(consumer.getFlushed()) >= 2
	})
	cancel()
	<-done

	if store.writeCount() != 1 {
		t.Fatalf("DLQ writes = %d, want 1", store.writeCount())
	}
	writes, err := store.List(context.Background(), dlq.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if writes[0].Seq != 1 {
		t.Fatalf("DLQ seq = %d, want 1", writes[0].Seq)
	}

	flushed := consumer.getFlushed()
	if len(flushed) != 2 || flushed[0] != 2 || flushed[1] != 3 {
		t.Fatalf("flushed = %v, want [2 3]", flushed)
	}
	if n := countSeq(consumer.getDelivered(), 1); n != 1 {
		t.Fatalf("seq 1 delivered %d times, want exactly 1 (pre-skip only)", n)
	}
	if got, ok := cs.lastSaved("poison-skip", 0); !ok || got != 4 {
		t.Fatalf("cursor = %d ok=%v, want 4", got, ok)
	}
}

func TestFlushBatchPoisonWindowPositions1And3(t *testing.T) {
	entries := []eventlog.LogEntry{
		poisonEntry(1, `"a"`),
		poisonEntry(2, `"b"`),
		poisonEntry(3, `"c"`),
		poisonEntry(4, `"d"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	store := &fakeDLQStore{}
	consumer := newPoisonBatchConsumer("poison-13", 1, 3)
	cs := newRecordingCursorStore()

	r := router.NewRouter(el, 1, cs)
	r.SetDLQ(store)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	waitUntil(t, 2500*time.Millisecond, func() bool {
		return store.writeCount() >= 2 && len(consumer.getFlushed()) >= 2
	})
	cancel()
	<-done

	if store.writeCount() != 2 {
		t.Fatalf("DLQ writes = %d, want 2", store.writeCount())
	}
	writes, _ := store.List(context.Background(), dlq.Filter{})
	seqs := map[uint64]bool{}
	for _, w := range writes {
		seqs[w.Seq] = true
	}
	if !seqs[1] || !seqs[3] {
		t.Fatalf("DLQ seqs = %v, want {1,3}", seqs)
	}

	flushed := consumer.getFlushed()
	if len(flushed) != 2 || flushed[0] != 2 || flushed[1] != 4 {
		t.Fatalf("flushed = %v, want [2 4]", flushed)
	}
	// seq 1: delivered once (first window) then skipped.
	// seq 3: delivered in the first window (before #1 was isolated) and again
	// in the second window (before its own poison flush), then skipped.
	if n := countSeq(consumer.getDelivered(), 1); n != 1 {
		t.Fatalf("seq 1 delivered %d times, want 1", n)
	}
	if n := countSeq(consumer.getDelivered(), 3); n != 2 {
		t.Fatalf("seq 3 delivered %d times, want 2", n)
	}
	if got, ok := cs.lastSaved("poison-13", 0); !ok || got != 5 {
		t.Fatalf("cursor = %d ok=%v, want 5", got, ok)
	}
}

func TestFlushBatchPoisonDLQDisabledTreatsAsTransient(t *testing.T) {
	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	entries := []eventlog.LogEntry{poisonEntry(1, `"x"`)}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	consumer := newPoisonBatchConsumer("poison-nodlq", 1)

	r := router.NewRouter(el, 1, nil)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Run(ctx)

	if len(consumer.getFlushed()) != 0 {
		t.Fatalf("flushed = %v, want empty (poison never skipped)", consumer.getFlushed())
	}
	// Under load, full-jitter backoff may allow only one attempt in a short
	// window; the durable contract is: never skip (no flush success) and log
	// once per seq.
	if n := countSeq(consumer.getDelivered(), 1); n < 1 {
		t.Fatalf("seq 1 delivered %d times, want >= 1", n)
	}
	logs := buf.String()
	const msg = "router: poison flush with DLQ disabled"
	if count := bytes.Count([]byte(logs), []byte(msg)); count != 1 {
		t.Fatalf("loud DLQ-disabled log count = %d, want 1; logs:\n%s", count, logs)
	}
}

func TestFlushBatchPoisonFailingStoreNoSkip(t *testing.T) {
	entries := []eventlog.LogEntry{
		poisonEntry(1, `"f1"`),
		poisonEntry(2, `"f2"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	store := &fakeDLQStore{failN: 100, failErr: errors.New("disk full")}
	metrics := observability.NewKaptantoMetrics()
	consumer := newPoisonBatchConsumer("poison-failstore", 1)

	r := router.NewRouter(el, 1, nil)
	r.SetDLQ(store)
	r.SetMetrics(metrics)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Run(ctx)

	if store.writeCount() != 0 {
		t.Fatalf("successful DLQ writes = %d, want 0", store.writeCount())
	}
	if len(consumer.getFlushed()) != 0 {
		t.Fatalf("flushed = %v, want empty (no skip without durable DLQ)", consumer.getFlushed())
	}
	if n := countSeq(consumer.getDelivered(), 1); n < 1 {
		t.Fatalf("seq 1 delivered %d times, want >= 1", n)
	}
	failN := testutil.ToFloat64(metrics.DLQWriteFailuresTotal.WithLabelValues("poison-failstore"))
	if failN < 1 {
		t.Fatalf("dlq_write_failures_total = %v, want >= 1", failN)
	}
}

func TestFlushBatchPoisonBackoffResetAfterSkip(t *testing.T) {
	entries := []eventlog.LogEntry{
		poisonEntry(1, `"b1"`),
		poisonEntry(2, `"b2"`),
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	store := &fakeDLQStore{}
	consumer := newPoisonBatchConsumer("poison-backoff", 1)

	r := router.NewRouter(el, 1, nil)
	r.SetDLQ(store)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)

	if store.writeCount() != 1 {
		t.Fatalf("DLQ writes = %d, want 1", store.writeCount())
	}
	flushed := consumer.getFlushed()
	if len(flushed) != 1 || flushed[0] != 2 {
		t.Fatalf("flushed = %v, want [2]", flushed)
	}
}

func TestFlushBatchPoisonStreakGuard(t *testing.T) {
	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	entries := make([]eventlog.LogEntry, 27)
	poisons := make([]uint64, 27)
	for i := 0; i < 27; i++ {
		seq := uint64(i + 1)
		key := string(rune('a' + i))
		entries[i] = poisonEntry(seq, `"`+key+`"`)
		poisons[i] = seq
	}
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{0: entries})
	store := &fakeDLQStore{}
	consumer := newPoisonBatchConsumer("poison-streak", poisons...)

	r := router.NewRouter(el, 1, nil)
	r.SetDLQ(store)
	r.Register(consumer)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	const streakMsg = "router: poison streak — suspected endpoint misconfiguration"
	waitUntil(t, 6*time.Second, func() bool {
		return store.writeCount() >= 25 && buf.contains(streakMsg)
	})

	time.Sleep(300 * time.Millisecond)
	if got := store.writeCount(); got != 25 {
		t.Fatalf("DLQ writes after streak = %d, want exactly 25", got)
	}
	if count := bytes.Count([]byte(buf.String()), []byte(streakMsg)); count != 1 {
		t.Fatalf("streak log count = %d, want 1; logs:\n%s", count, buf.String())
	}
	cancel()
	<-done
}
