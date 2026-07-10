package router_test

import (
	"context"
	"errors"
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

type permanentFailConsumer struct {
	id    string
	calls int
}

func (c *permanentFailConsumer) ID() string { return c.id }

func (c *permanentFailConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	c.calls++
	return &router.PermanentFlushError{Seq: entry.Seq, Cause: errors.New("poison event")}
}

type fakeDLQStore struct {
	mu        sync.Mutex
	writes    []dlq.Entry
	failErr   error
	failN     int
	entered   chan struct{}
	release   chan struct{}
	blockOnce sync.Once
}

func (f *fakeDLQStore) Write(_ context.Context, e dlq.Entry) error {
	if f.entered != nil {
		f.blockOnce.Do(func() { close(f.entered) })
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failN > 0 {
		f.failN--
		err := f.failErr
		if err == nil {
			err = errors.New("dlq write failed")
		}
		return err
	}
	for _, existing := range f.writes {
		if existing.ConsumerID == e.ConsumerID && existing.EventID == e.EventID {
			return nil
		}
	}
	f.writes = append(f.writes, e)
	return nil
}

func (f *fakeDLQStore) List(context.Context, dlq.Filter) ([]dlq.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dlq.Entry, len(f.writes))
	copy(out, f.writes)
	return out, nil
}
func (f *fakeDLQStore) Get(context.Context, string) (dlq.Entry, error) {
	return dlq.Entry{}, dlq.ErrNotFound
}
func (f *fakeDLQStore) Delete(context.Context, ...string) error        { return nil }
func (f *fakeDLQStore) Purge(context.Context, dlq.Filter) (int, error) { return 0, nil }
func (f *fakeDLQStore) Close() error                                   { return nil }

func (f *fakeDLQStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func dlqTestEntry(seq uint64, keyJSON string) eventlog.LogEntry {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	return eventlog.LogEntry{
		Seq:         seq,
		PartitionID: 3,
		Raw:         []byte(`{"id":1}`),
		Event: &event.ChangeEvent{
			ID:             id,
			Table:          "orders",
			Key:            []byte(keyJSON),
			IdempotencyKey: "pg:public.orders:1:insert:0/1",
		},
	}
}

func TestDeadLetterPersistsToDLQAndPops(t *testing.T) {
	store := &fakeDLQStore{}
	metrics := observability.NewKaptantoMetrics()
	rs := router.NewRetryScheduler()
	rs.SetDLQ(store)
	rs.SetMetrics(metrics)

	consumer := &permanentFailConsumer{id: "c-dlq-ok"}
	entry := dlqTestEntry(10, `{"id":1}`)
	rec := &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec)

	rs.Tick(context.Background())

	if rs.BlockedCount(consumer) != 0 {
		t.Fatal("expected head popped after successful DLQ write")
	}
	if store.writeCount() != 1 {
		t.Fatalf("expected 1 DLQ write, got %d", store.writeCount())
	}
	got := store.writes[0]
	if got.ConsumerID != consumer.id || got.EventID != entry.Event.ID.String() {
		t.Fatalf("unexpected DLQ entry: %+v", got)
	}
	if got.Seq != 10 || got.PartitionID != 3 || got.Table != "orders" {
		t.Fatalf("unexpected DLQ fields: %+v", got)
	}
	if string(got.Payload) != `{"id":1}` {
		t.Fatalf("payload = %q, want raw", got.Payload)
	}
	if got.Reason == "" {
		t.Fatal("expected non-empty reason from LastErr")
	}
	if rec.LastErr == nil {
		t.Fatal("expected LastErr populated by drainGroup")
	}
	if n := testutil.ToFloat64(metrics.DLQEventsTotal.WithLabelValues(consumer.id)); n != 1 {
		t.Fatalf("dlq_events_total = %v, want 1", n)
	}
	if _, ok := rs.Floor(consumer.id, 3); ok {
		t.Fatal("expected floor cleared after pop")
	}
}

func TestDeadLetterDLQWriteFailureKeepsHead(t *testing.T) {
	store := &fakeDLQStore{failN: 1, failErr: errors.New("disk full")}
	metrics := observability.NewKaptantoMetrics()
	rs := router.NewRetryScheduler()
	rs.SetDLQ(store)
	rs.SetMetrics(metrics)

	consumer := &permanentFailConsumer{id: "c-dlq-fail"}
	entry := dlqTestEntry(20, `{"id":2}`)
	entry.PartitionID = 5
	rec := &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec)

	beforeFloor, beforeOK := rs.Floor(consumer.id, 5)
	if !beforeOK || beforeFloor != 20 {
		t.Fatalf("floor before = %d ok=%v", beforeFloor, beforeOK)
	}

	rs.Tick(context.Background())

	if rs.BlockedCount(consumer) != 1 {
		t.Fatal("DLQ-01: head must stay when Write fails")
	}
	if store.writeCount() != 0 {
		t.Fatal("failed Write must not persist an entry")
	}
	afterFloor, afterOK := rs.Floor(consumer.id, 5)
	if !afterOK || afterFloor != beforeFloor {
		t.Fatalf("floor must stay pinned: got %d ok=%v", afterFloor, afterOK)
	}
	if !rec.NextRetryAt.After(time.Now()) {
		t.Fatalf("NextRetryAt must be pushed to plateau, got %v", rec.NextRetryAt)
	}
	if n := testutil.ToFloat64(metrics.DLQWriteFailuresTotal.WithLabelValues(consumer.id)); n != 1 {
		t.Fatalf("dlq_write_failures_total = %v, want 1", n)
	}

	rs.ForceRetryNow(consumer, "gk")
	rs.Tick(context.Background())
	if rs.BlockedCount(consumer) != 0 {
		t.Fatal("expected pop after successful DLQ write")
	}
	if store.writeCount() != 1 {
		t.Fatalf("expected 1 persisted entry, got %d", store.writeCount())
	}
	if n := testutil.ToFloat64(metrics.DLQEventsTotal.WithLabelValues(consumer.id)); n != 1 {
		t.Fatalf("dlq_events_total = %v, want 1", n)
	}
}

func TestDeadLetterCrashConvergenceDedups(t *testing.T) {
	store := &fakeDLQStore{}
	rs := router.NewRetryScheduler()
	rs.SetDLQ(store)

	consumer := &permanentFailConsumer{id: "c-crash"}
	entry := dlqTestEntry(40, `{"id":5}`)
	rec := &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec)
	rs.Tick(context.Background())
	if store.writeCount() != 1 {
		t.Fatalf("first write count = %d", store.writeCount())
	}

	rec2 := &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec2)
	rs.Tick(context.Background())

	if store.writeCount() != 1 {
		t.Fatalf("dedup: logical rows = %d, want 1", store.writeCount())
	}
}

func TestDeadLetterExhaustionWithDLQ(t *testing.T) {
	store := &fakeDLQStore{}
	metrics := observability.NewKaptantoMetrics()
	rs := router.NewRetryScheduler()
	rs.SetDLQ(store)
	rs.SetMetrics(metrics)

	consumer := &alwaysFailConsumer{id: "c-exhaust"}
	entry := dlqTestEntry(50, `{"id":6}`)
	rec := &router.RetryRecord{
		Entry:       entry,
		Attempts:    14,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec)
	rs.Tick(context.Background())

	if rs.BlockedCount(consumer) != 0 {
		t.Fatal("expected pop after exhaustion + DLQ write")
	}
	if store.writeCount() != 1 {
		t.Fatalf("writes = %d", store.writeCount())
	}
	if rec.LastErr == nil {
		t.Fatal("LastErr must be set")
	}
	if n := testutil.ToFloat64(metrics.DLQEventsTotal.WithLabelValues(consumer.id)); n != 1 {
		t.Fatalf("metric = %v", n)
	}
}
