package ha_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/ha"
)

func skipIfNoDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run HA leader election tests")
	}
	return dsn
}

// TestLeaderTryAcquire_NoContention verifies a single connection can acquire
// the advisory lock when no other session holds it.
func TestLeaderTryAcquire_NoContention(t *testing.T) {
	dsn := skipIfNoDSN(t)
	ctx := context.Background()

	le, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector: %v", err)
	}
	defer le.Close()

	got, err := le.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !got {
		t.Fatal("expected TryAcquire to return true (no contention), got false")
	}
}

// TestLeaderTryAcquire_Contention verifies that a second connection cannot
// acquire the advisory lock while the first connection holds it.
func TestLeaderTryAcquire_Contention(t *testing.T) {
	dsn := skipIfNoDSN(t)
	ctx := context.Background()

	le1, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le1: %v", err)
	}
	defer le1.Close()

	ok, err := le1.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("le1.TryAcquire: %v", err)
	}
	if !ok {
		t.Fatal("le1 should have acquired lock")
	}

	le2, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le2: %v", err)
	}
	defer le2.Close()

	ok2, err := le2.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("le2.TryAcquire: %v", err)
	}
	if ok2 {
		t.Fatal("expected le2 TryAcquire to return false (lock held by le1), got true")
	}
}

// TestLeaderRelease verifies that after Release(), a second connection can
// acquire the advisory lock.
func TestLeaderRelease(t *testing.T) {
	dsn := skipIfNoDSN(t)
	ctx := context.Background()

	le1, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le1: %v", err)
	}
	defer le1.Close()

	ok, err := le1.TryAcquire(ctx)
	if err != nil || !ok {
		t.Fatalf("le1 acquire: ok=%v err=%v", ok, err)
	}

	if err := le1.Release(ctx); err != nil {
		t.Fatalf("le1.Release: %v", err)
	}

	le2, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le2: %v", err)
	}
	defer le2.Close()

	ok2, err := le2.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("le2.TryAcquire after release: %v", err)
	}
	if !ok2 {
		t.Fatal("expected le2 TryAcquire to return true after le1 released, got false")
	}
}

// TestLeaderRunStandby_TakeoverOnClose verifies that RunStandby returns nil
// (signalling "I am now leader") after the leader closes its connection,
// simulating a crash takeover.
func TestLeaderRunStandby_TakeoverOnClose(t *testing.T) {
	dsn := skipIfNoDSN(t)
	ctx := context.Background()

	// le1 is the "leader" — holds the lock.
	le1, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le1: %v", err)
	}

	ok, err := le1.TryAcquire(ctx)
	if err != nil || !ok {
		t.Fatalf("le1 acquire: ok=%v err=%v", ok, err)
	}

	// le2 is the "standby" — will poll until le1 closes.
	le2, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le2: %v", err)
	}
	defer le2.Close()

	// Release leader after 150ms to simulate crash.
	go func() {
		time.Sleep(150 * time.Millisecond)
		le1.Close()
	}()

	standbyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	err = le2.RunStandby(standbyCtx, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("RunStandby expected nil (lock acquired), got: %v", err)
	}
}

// TestLeaderRunStandby_CtxCancellation verifies that RunStandby returns
// ctx.Err() when the context is cancelled before the lock is acquired.
func TestLeaderRunStandby_CtxCancellation(t *testing.T) {
	dsn := skipIfNoDSN(t)
	ctx := context.Background()

	// le1 holds the lock for the full duration of this test.
	le1, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le1: %v", err)
	}
	defer le1.Close()

	ok, err := le1.TryAcquire(ctx)
	if err != nil || !ok {
		t.Fatalf("le1 acquire: ok=%v err=%v", ok, err)
	}

	le2, err := ha.NewLeaderElector(ctx, dsn)
	if err != nil {
		t.Fatalf("NewLeaderElector le2: %v", err)
	}
	defer le2.Close()

	standbyCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	err = le2.RunStandby(standbyCtx, 50*time.Millisecond)
	if err == nil {
		t.Fatal("RunStandby expected ctx error, got nil")
	}
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("RunStandby expected context error, got: %v", err)
	}
}
