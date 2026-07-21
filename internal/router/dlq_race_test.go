package router

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
)

type raceFailConsumer struct {
	id string
}

func (c *raceFailConsumer) ID() string { return c.id }

func (c *raceFailConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	return &PermanentFlushError{Seq: entry.Seq, Cause: errors.New("poison")}
}

type blockingDLQ struct {
	mu      sync.Mutex
	writes  int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingDLQ) Write(_ context.Context, _ dlq.Entry) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.mu.Lock()
	b.writes++
	b.mu.Unlock()
	return nil
}
func (b *blockingDLQ) List(context.Context, dlq.Filter) ([]dlq.Entry, error) { return nil, nil }
func (b *blockingDLQ) Get(context.Context, string) (dlq.Entry, error) {
	return dlq.Entry{}, dlq.ErrNotFound
}
func (b *blockingDLQ) Delete(context.Context, ...string) error        { return nil }
func (b *blockingDLQ) Purge(context.Context, dlq.Filter) (int, error) { return 0, nil }
func (b *blockingDLQ) Close() error                                   { return nil }

func TestDeadLetterHeadChangedRaceBails(t *testing.T) {
	store := &blockingDLQ{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	rs := NewRetryScheduler()
	rs.SetDLQ(store)

	consumer := &raceFailConsumer{id: "c-race"}
	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	entry := eventlog.LogEntry{
		Seq:         30,
		PartitionID: 1,
		Raw:         []byte(`{}`),
		Event: &event.ChangeEvent{
			ID:    id,
			Table: "t",
			Key:   []byte(`{"id":3}`),
		},
	}
	rec := &RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  consumer.id,
	}
	rs.AddBlocked(consumer, "gk", rec)

	done := make(chan struct{})
	go func() {
		rs.Tick(context.Background())
		close(done)
	}()

	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Write never entered")
	}

	replacement := &RetryRecord{
		Entry: eventlog.LogEntry{
			Seq:         99,
			PartitionID: 1,
			Event: &event.ChangeEvent{
				ID:    ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy()),
				Table: "t",
				Key:   []byte(`{"id":99}`),
			},
		},
		Attempts:    1,
		NextRetryAt: time.Now().Add(time.Hour),
		ConsumerID:  consumer.id,
	}
	rs.mu.Lock()
	s := rs.states[consumer.id]
	s.blockedGroups["gk"] = []*RetryRecord{replacement}
	rs.recomputeFloorLocked(s, 1)
	rs.mu.Unlock()

	close(store.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Tick did not finish")
	}

	if rs.BlockedCount(consumer) != 1 {
		t.Fatalf("blocked = %d, want 1 (replacement head)", rs.BlockedCount(consumer))
	}
	rs.mu.Lock()
	head := rs.states[consumer.id].blockedGroups["gk"][0]
	rs.mu.Unlock()
	if head != replacement {
		t.Fatal("expected replacement head to remain after race bail")
	}
	if head.Entry.Seq != 99 {
		t.Fatalf("head seq = %d", head.Entry.Seq)
	}
}
