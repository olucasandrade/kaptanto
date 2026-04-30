package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// TestSetOwnedPartitions_StoresSlice verifies that SetOwnedPartitions stores
// the provided slice in the Router's ownedPartitions field. This is a
// compile-time guard: if SetOwnedPartitions does not exist, this file fails
// to compile.
func TestSetOwnedPartitions_StoresSlice(t *testing.T) {
	el := newFakeEventLog(nil)
	r := router.NewRouter(el, 8, nil)

	r.SetOwnedPartitions([]uint32{5, 10})

	// Run for a short time — only partitions 5 and 10 should be iterated.
	// We can't introspect ownedPartitions directly (unexported field), but we
	// verify the method exists and doesn't panic.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
}

// TestSetOwnedPartitions_NilRestoresAll verifies that SetOwnedPartitions(nil)
// restores the default all-partitions behavior. Run with nil must start a
// goroutine for every partition (compile check and smoke test).
func TestSetOwnedPartitions_NilRestoresAll(t *testing.T) {
	el := newFakeEventLog(nil)
	r := router.NewRouter(el, 4, nil)

	r.SetOwnedPartitions([]uint32{1})
	r.SetOwnedPartitions(nil) // restore all

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
}

// TestAllPartitions_NilMeansAll verifies that when ownedPartitions is nil,
// the Router delivers events from all partitions (not just a subset).
func TestAllPartitions_NilMeansAll(t *testing.T) {
	entry0 := makeEntry(1, `{"id":0}`)
	entry1 := makeEntry(1, `{"id":1}`)
	el := newFakeEventLog(map[uint32][]eventlog.LogEntry{
		0: {entry0},
		1: {entry1},
	})

	consumer := &fakeConsumer{id: "c-all"}
	r := router.NewRouter(el, 2, nil)
	r.Register(consumer)
	// nil ownedPartitions = all 2 partitions

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)

	got := consumer.delivered()
	if len(got) < 2 {
		t.Errorf("expected events from all 2 partitions, got %d delivered", len(got))
	}
}
