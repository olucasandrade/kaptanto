package router_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// TestNextDelay verifies that nextDelay returns the correct backoff duration for
// each attempt index, including the plateau at attempts >= len(retryDelays).
func TestNextDelay(t *testing.T) {
	cases := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 5 * time.Second},
		{2, 30 * time.Second},
		{3, 2 * time.Minute},
		{4, 10 * time.Minute},
		{5, 10 * time.Minute},  // plateau
		{99, 10 * time.Minute}, // far beyond — still plateau
	}

	for _, tc := range cases {
		got := router.NextDelay(tc.attempt)
		if got != tc.expected {
			t.Errorf("NextDelay(%d) = %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

// errRetry is a sentinel delivery error for retry tests.
var errRetry = errors.New("delivery error")

// countingConsumer records how many times Deliver is called and returns an
// error for the first `failFor` calls, then nil.
type countingConsumer struct {
	id      string
	failFor int
	calls   int
	entries []eventlog.LogEntry
}

func (c *countingConsumer) ID() string { return c.id }

func (c *countingConsumer) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	c.calls++
	if c.calls <= c.failFor {
		return errRetry
	}
	c.entries = append(c.entries, entry)
	return nil
}

// alwaysFailConsumer always returns an error on Deliver.
type alwaysFailConsumer struct {
	id    string
	calls int
}

func (c *alwaysFailConsumer) ID() string { return c.id }

func (c *alwaysFailConsumer) Deliver(_ context.Context, _ eventlog.LogEntry) error {
	c.calls++
	return errRetry
}

// TestRetrySchedulerRetriesOnFailure verifies that RetryScheduler.Tick re-attempts
// blocked entries and clears the blocked group on successful delivery.
func TestRetrySchedulerRetriesOnFailure(t *testing.T) {
	consumer := &countingConsumer{id: "c1", failFor: 2}
	rs := router.NewRetryScheduler()

	// Seed the retry scheduler with a blocked entry.
	entry := makeEntry(1, `{"id":1}`)
	rs.AddBlocked(consumer, "group-key", &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second), // force immediate retry
		ConsumerID:  "c1",
	})

	ctx := context.Background()

	// Tick 1 — consumer fails (2nd call overall, failFor=2)
	rs.Tick(ctx)
	if rs.BlockedCount(consumer) == 0 {
		t.Fatal("expected entry still blocked after tick 1")
	}

	// Tick 2 — consumer still fails (but nextRetryAt must be reset to past)
	rs.ForceRetryNow(consumer, "group-key")
	rs.Tick(ctx)
	if rs.BlockedCount(consumer) == 0 {
		t.Fatal("expected entry still blocked after tick 2")
	}

	// Tick 3 — consumer succeeds (3rd call overall, failFor=2 so 3rd succeeds)
	rs.ForceRetryNow(consumer, "group-key")
	rs.Tick(ctx)
	if rs.BlockedCount(consumer) != 0 {
		t.Fatalf("expected blocked group cleared after successful retry, got %d blocked", rs.BlockedCount(consumer))
	}
}

// TestRetrySchedulerDeadLettersAfterMaxRetries verifies that after maxRetries
// failed attempts, the entry is removed from blockedGroups (dead-lettered).
func TestRetrySchedulerDeadLettersAfterMaxRetries(t *testing.T) {
	consumer := &alwaysFailConsumer{id: "c2"}
	rs := router.NewRetryScheduler()

	entry := makeEntry(2, `{"id":2}`)
	rs.AddBlocked(consumer, "group-key", &router.RetryRecord{
		Entry:       entry,
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Second),
		ConsumerID:  "c2",
	})

	ctx := context.Background()

	// Tick maxRetries times, forcing nextRetryAt to past each time.
	const maxRetries = 15
	for i := 0; i < maxRetries; i++ {
		rs.ForceRetryNow(consumer, "group-key")
		rs.Tick(ctx)
	}

	// After maxRetries failures the entry must be dead-lettered (removed).
	if rs.BlockedCount(consumer) != 0 {
		t.Fatalf("expected dead-lettered after %d attempts, got %d blocked", maxRetries, rs.BlockedCount(consumer))
	}
}
