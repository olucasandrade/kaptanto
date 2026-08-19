package postgres

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/event"
)

type stubCheckpointStore struct{}

func (stubCheckpointStore) Save(context.Context, string, string) error   { return nil }
func (stubCheckpointStore) Load(context.Context, string) (string, error) { return "", nil }
func (stubCheckpointStore) Close() error                                 { return nil }

var _ checkpoint.CheckpointStore = stubCheckpointStore{}

type recordingBackfillEngine struct {
	resetCalls int32
	pending    bool
	resetLSN   uint64
	runStarted chan struct{}
}

func (r *recordingBackfillEngine) Run(_ context.Context) error {
	if r.runStarted != nil {
		select {
		case <-r.runStarted:
		default:
			close(r.runStarted)
		}
	}
	return nil
}

func (r *recordingBackfillEngine) HasPendingBackfills() bool { return r.pending }

func (r *recordingBackfillEngine) ResetForSlotLoss(_ context.Context, lsn uint64) error {
	atomic.AddInt32(&r.resetCalls, 1)
	r.resetLSN = lsn
	r.pending = true
	return nil
}

func newTestConnector(t *testing.T, eng *recordingBackfillEngine) *PostgresConnector {
	t.Helper()
	cfg := Config{DSN: "postgres://localhost/testdb", SourceID: "pg1"}
	c := New(cfg, stubCheckpointStore{}, event.NewIDGenerator())
	if eng != nil {
		c.SetBackfillEngine(eng)
	}
	return c
}

func TestResetBackfillOnSlotLoss_OnlyWhenNeedsSnapshot(t *testing.T) {
	eng := &recordingBackfillEngine{}
	c := newTestConnector(t, eng)

	if err := c.resetBackfillOnSlotLoss(context.Background(), false, 99); err != nil {
		t.Fatalf("ordinary reconnect reset: %v", err)
	}
	if atomic.LoadInt32(&eng.resetCalls) != 0 {
		t.Fatal("ordinary reconnect must not reset backfill state")
	}

	if err := c.resetBackfillOnSlotLoss(context.Background(), true, 99); err != nil {
		t.Fatalf("slot-loss reset: %v", err)
	}
	if atomic.LoadInt32(&eng.resetCalls) != 1 {
		t.Fatalf("slot-loss reset calls = %d, want 1", eng.resetCalls)
	}
	if eng.resetLSN != 99 {
		t.Fatalf("reset LSN = %d, want 99", eng.resetLSN)
	}
}

func TestResetBackfillOnSlotLoss_NilEngine(t *testing.T) {
	c := newTestConnector(t, nil)
	if err := c.resetBackfillOnSlotLoss(context.Background(), true, 1); err != nil {
		t.Fatalf("nil engine: %v", err)
	}
}

func TestLaunchBackfill_SlotLossLaunchesWithoutPending(t *testing.T) {
	eng := &recordingBackfillEngine{runStarted: make(chan struct{})}
	c := newTestConnector(t, eng)

	c.launchBackfill(context.Background(), true)

	select {
	case <-eng.runStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slot-loss must launch Run even when HasPendingBackfills is false")
	}
}

func TestLaunchBackfill_OrdinaryReconnectWithoutPendingDoesNotLaunch(t *testing.T) {
	eng := &recordingBackfillEngine{runStarted: make(chan struct{})}
	c := newTestConnector(t, eng)

	c.launchBackfill(context.Background(), false)

	select {
	case <-eng.runStarted:
		t.Fatal("ordinary reconnect with no pending backfills must not launch Run")
	case <-time.After(50 * time.Millisecond):
	}
}
