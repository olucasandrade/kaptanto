package action

import (
	"context"
	"sync/atomic"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/olucasandrade/kaptanto/internal/routing"
)

// MatcherForTest is a test wrapper around routing.Matcher for use in tests.
type MatcherForTest = routing.Matcher

// NewMatcherForTest compiles a matcher for test use.
func NewMatcherForTest(tables, ops []string) *routing.Matcher {
	m, err := routing.Compile(routing.MatchConfig{Tables: tables, Operations: ops})
	if err != nil {
		panic(err)
	}
	return m
}

// TestMatchConsumer exposes matchConsumer for tests. Returns a wrapper that
// allows inspecting delivery count on the inner consumer.
type TestMatchConsumer struct {
	inner *matchConsumer
	count *atomic.Int64
}

// NewMatchConsumer creates a matchConsumer exposed for testing.
func NewMatchConsumer(inner router.Consumer, matcher *routing.Matcher, m *observability.KaptantoMetrics, id string) *TestMatchConsumer {
	mc := &matchConsumer{
		inner:   inner,
		matcher: matcher,
		m:       m,
		id:      id,
	}
	return &TestMatchConsumer{inner: mc, count: &atomic.Int64{}}
}

func (t *TestMatchConsumer) ID() string { return t.inner.ID() }
func (t *TestMatchConsumer) Deliver(ctx context.Context, e eventlog.LogEntry) error {
	return t.inner.Deliver(ctx, e)
}
func (t *TestMatchConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	return t.inner.FlushBatch(ctx, partitionID)
}
func (t *TestMatchConsumer) Close() { t.inner.Close() }
func (t *TestMatchConsumer) Ping() error { return t.inner.Ping() }
func (t *TestMatchConsumer) SetMetrics(m *observability.KaptantoMetrics) { t.inner.SetMetrics(m) }
func (t *TestMatchConsumer) InnerDeliverCount() int64 {
	type deliverCounter interface {
		DeliverCount() int64
	}
	if dc, ok := t.inner.inner.(deliverCounter); ok {
		return dc.DeliverCount()
	}
	return 0
}
