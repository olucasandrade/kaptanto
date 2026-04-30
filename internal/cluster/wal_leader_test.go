package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natstest "github.com/nats-io/nats-server/v2/test"
)

// startTestNATSForCluster starts an in-process single-node NATS server with JetStream
// enabled, returning a connected *nats.Conn. The server and connection are cleaned up
// via t.Cleanup. This mirrors the helper in eventlog/nats_test.go.
func startTestNATSForCluster(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL(), nats.Name("test-cluster"))
	if err != nil {
		t.Fatalf("startTestNATSForCluster: connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// TestNewWalLeaderElector_ValidInputs verifies that NewWalLeaderElector with a valid
// *nats.Conn and nodeID returns a non-nil elector with no error.
func TestNewWalLeaderElector_ValidInputs(t *testing.T) {
	nc := startTestNATSForCluster(t)
	ctx := context.Background()

	el, err := NewWalLeaderElector(ctx, nc, "node-1")
	if err != nil {
		t.Fatalf("NewWalLeaderElector() unexpected error: %v", err)
	}
	if el == nil {
		t.Fatal("NewWalLeaderElector() returned nil elector")
	}
}

// TestWalLeaderElector_WinsLease verifies that after Run wins the lease (Create succeeds),
// EpochGetter() returns (rev>0, true).
func TestWalLeaderElector_WinsLease(t *testing.T) {
	nc := startTestNATSForCluster(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	el, err := NewWalLeaderElector(ctx, nc, "node-1")
	if err != nil {
		t.Fatalf("NewWalLeaderElector() error: %v", err)
	}

	// Run in background; cancel after we verify.
	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- el.Run(runCtx)
	}()

	// Wait until el.isLeader becomes true (up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, isLeader := el.EpochGetter()
		if isLeader {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	epoch, isLeader := el.EpochGetter()
	if !isLeader {
		t.Error("EpochGetter() isLeader must be true after winning the lease")
	}
	if epoch == 0 {
		t.Error("EpochGetter() epoch must be > 0 after winning the lease")
	}

	runCancel()
	<-done
}

// TestWalLeaderElector_LosesLeaseToContender verifies that when two electors race for
// the same KV key, exactly one wins and the other sets isLeader=false.
func TestWalLeaderElector_LosesLeaseToContender(t *testing.T) {
	nc := startTestNATSForCluster(t)
	ctx := context.Background()

	el1, err := NewWalLeaderElector(ctx, nc, "node-1")
	if err != nil {
		t.Fatalf("NewWalLeaderElector(node-1) error: %v", err)
	}
	el2, err := NewWalLeaderElector(ctx, nc, "node-2")
	if err != nil {
		t.Fatalf("NewWalLeaderElector(node-2) error: %v", err)
	}

	run1Ctx, run1Cancel := context.WithCancel(context.Background())
	run2Ctx, run2Cancel := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)

	go func() { done1 <- el1.Run(run1Ctx) }()
	go func() { done2 <- el2.Run(run2Ctx) }()

	// Wait until one of them wins (up to 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, l1 := el1.EpochGetter()
		_, l2 := el2.EpochGetter()
		if l1 || l2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, isLeader1 := el1.EpochGetter()
	_, isLeader2 := el2.EpochGetter()

	// Exactly one must be leader, the other must be follower.
	if isLeader1 && isLeader2 {
		t.Error("both electors claim to be leader — split-brain violation")
	}
	if !isLeader1 && !isLeader2 {
		t.Error("neither elector won the lease within the timeout")
	}

	run1Cancel()
	run2Cancel()
	<-done1
	<-done2
}

// TestWalLeaderElector_CancelSetsNotLeader verifies that after the leader's context is
// cancelled, isLeader becomes false.
func TestWalLeaderElector_CancelSetsNotLeader(t *testing.T) {
	nc := startTestNATSForCluster(t)

	el, err := NewWalLeaderElector(context.Background(), nc, "node-1")
	if err != nil {
		t.Fatalf("NewWalLeaderElector() error: %v", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- el.Run(runCtx)
	}()

	// Wait until leader.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, isLeader := el.EpochGetter()
		if isLeader {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, isLeaderBefore := el.EpochGetter()
	if !isLeaderBefore {
		t.Fatal("elector did not win lease before cancel; test precondition failed")
	}

	// Cancel the context.
	runCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s after context cancel")
	}

	_, isLeaderAfter := el.EpochGetter()
	if isLeaderAfter {
		t.Error("EpochGetter() isLeader must be false after context cancel")
	}
}

// TestWalLeaderElector_DefaultTTLsSet verifies that NewWalLeaderElector applies the
// correct default leaseTTL and renewEvery values.
func TestWalLeaderElector_DefaultTTLsSet(t *testing.T) {
	nc := startTestNATSForCluster(t)
	ctx := context.Background()

	el, err := NewWalLeaderElector(ctx, nc, "node-defaults")
	if err != nil {
		t.Fatalf("NewWalLeaderElector() error: %v", err)
	}

	if el.leaseTTL != defaultLeaseTTL {
		t.Errorf("leaseTTL = %v, want %v", el.leaseTTL, defaultLeaseTTL)
	}
	if el.renewEvery != defaultRenewEvery {
		t.Errorf("renewEvery = %v, want %v", el.renewEvery, defaultRenewEvery)
	}
	// TTL must be >= 2× renewEvery to handle one missed renewal.
	if el.leaseTTL < 2*el.renewEvery {
		t.Errorf("leaseTTL (%v) must be >= 2×renewEvery (%v)", el.leaseTTL, 2*el.renewEvery)
	}
}
