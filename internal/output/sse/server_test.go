package sse

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/checkpoint"
	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/router"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// fakeRouter is a minimal router stub for SSE tests.
// Register calls the consumer's Deliver once with a test event then returns.
type fakeRouter struct {
	cs          router.ConsumerCursorStore
	testEvent   *event.ChangeEvent
	numPartitions uint32
}

func newFakeRouter(cs router.ConsumerCursorStore) *fakeRouter {
	return &fakeRouter{
		cs:            cs,
		numPartitions: 1,
		testEvent: &event.ChangeEvent{
			ID:             ulid.Make(),
			IdempotencyKey: "test:orders.orders:1:insert:0/0",
			Operation:      event.OpInsert,
			Table:          "orders",
		},
	}
}

// Register mimics the real Router: load cursor for each partition, deliver one
// event, then flush (matching the batch-flush path added by Fix E).
func (f *fakeRouter) Register(c router.Consumer) {
	ctx := context.Background()
	for p := uint32(0); p < f.numPartitions; p++ {
		seq, err := f.cs.LoadCursor(ctx, c.ID(), p)
		if err != nil {
			seq = 1
		}
		entry := eventlog.LogEntry{Seq: seq, Event: f.testEvent}
		_ = c.Deliver(ctx, entry)
	}
	// Flush buffered writes so the HTTP client sees the response (Fix E).
	if bf, ok := c.(router.BatchFlusher); ok {
		_ = bf.FlushBatch(ctx)
	}
}

// fakeRouterNoDeliver is a router stub that registers without delivering any events.
type fakeRouterNoDeliver struct {
	mu         sync.Mutex
	registered []string
}

func (f *fakeRouterNoDeliver) Register(c router.Consumer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, c.ID())
}

// Registered returns a safe copy of the registered consumer IDs.
func (f *fakeRouterNoDeliver) Registered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.registered))
	copy(out, f.registered)
	return out
}

// wrapForSSEServer wraps a fakeRouter to satisfy the *router.Router type required by SSEServer.
// Since SSEServer accepts *router.Router, we test SSEConsumer + SSEServer together via
// an httptest.Server and a test-only SSEServer variant that takes a registerFunc.
// For tests that need the full server, we build a real *router.Router with a noop eventlog.

type routerIface interface {
	Register(router.Consumer)
}

// testSSEServer is a test-only variant of SSEServer that accepts a routerIface.
type testSSEServer struct {
	r            routerIface
	cursorStore  router.ConsumerCursorStore
	corsOrigin   string
	pingInterval time.Duration
}

func newTestSSEServer(r routerIface, cs router.ConsumerCursorStore, corsOrigin string, pingInterval time.Duration) *testSSEServer {
	if corsOrigin == "" {
		corsOrigin = "*"
	}
	if pingInterval == 0 {
		pingInterval = 15 * time.Second
	}
	return &testSSEServer{r: r, cursorStore: cs, corsOrigin: corsOrigin, pingInterval: pingInterval}
}

func (s *testSSEServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)

	consumerID := r.URL.Query().Get("consumer")
	if consumerID == "" {
		consumerID = r.RemoteAddr
	}

	tables := filterNonEmpty(strings.Split(r.URL.Query().Get("tables"), ","))
	ops := filterNonEmpty(strings.Split(r.URL.Query().Get("operations"), ","))
	filter := output.NewEventFilter(tables, ops)

	consumer := NewSSEConsumer(consumerID, w, filter, s.cursorStore, nil, nil, nil)

	lastEventID := r.Header.Get("Last-Event-ID")
	_ = lastEventID

	s.r.Register(consumer)

	pingTicker := time.NewTicker(s.pingInterval)
	defer pingTicker.Stop()

	rc := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingTicker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			_ = rc.Flush()
		}
	}
}

func newInMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// Test 1: GET /stream response has Content-Type: text/event-stream
func TestSSEServer_ContentTypeHeader(t *testing.T) {
	cs := router.NewNoopCursorStore()
	fr := newFakeRouter(cs)
	srv := httptest.NewServer(newTestSSEServer(fr, cs, "*", time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?consumer=test1", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

// Test 2: GET /stream response has Access-Control-Allow-Origin: configured-origin
func TestSSEServer_CORSHeader(t *testing.T) {
	cs := router.NewNoopCursorStore()
	fr := newFakeRouter(cs)
	srv := httptest.NewServer(newTestSSEServer(fr, cs, "https://example.com", time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?consumer=test2", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "https://example.com", resp.Header.Get("Access-Control-Allow-Origin"))
}

// Test 3: SSEServer sends ": ping\n\n" on the ping interval
func TestSSEServer_PingKeepalive(t *testing.T) {
	cs := router.NewNoopCursorStore()
	// Use a router that doesn't deliver events so we don't get SSE data mixed with pings.
	fr := &fakeRouterNoDeliver{}
	pingInterval := 50 * time.Millisecond
	srv := httptest.NewServer(newTestSSEServer(fr, cs, "*", pingInterval))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?consumer=test3", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Read lines until we see a ping comment or timeout.
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == ": ping" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected to receive a SSE ping comment line")
}

// Test 4: When client context is cancelled, ServeHTTP returns (no goroutine leak)
func TestSSEServer_ContextCancellation(t *testing.T) {
	cs := router.NewNoopCursorStore()
	fr := &fakeRouterNoDeliver{}
	srv := httptest.NewServer(newTestSSEServer(fr, cs, "*", time.Hour))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?consumer=test4", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Cancel the client context after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// goroutine exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancellation")
	}
}

// Test 5: Two concurrent GET /stream requests each receive their own SSEConsumer
func TestSSEServer_IndependentConsumers(t *testing.T) {
	cs := router.NewNoopCursorStore()
	fr := &fakeRouterNoDeliver{}
	srv := httptest.NewServer(newTestSSEServer(fr, cs, "*", time.Hour))
	defer srv.Close()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())

	req1, _ := http.NewRequestWithContext(ctx1, "GET", srv.URL+"?consumer=consumer-a", nil)
	req2, _ := http.NewRequestWithContext(ctx2, "GET", srv.URL+"?consumer=consumer-b", nil)

	// Launch both requests concurrently; cancel them after a brief pause to allow
	// both ServeHTTP handlers to reach their ping loop (i.e. both have registered).
	errs := make(chan error, 2)
	go func() {
		resp, err := http.DefaultClient.Do(req1)
		if resp != nil {
			resp.Body.Close()
		}
		errs <- err
	}()
	go func() {
		resp, err := http.DefaultClient.Do(req2)
		if resp != nil {
			resp.Body.Close()
		}
		errs <- err
	}()

	// Give handlers time to register, then cancel both contexts.
	time.Sleep(100 * time.Millisecond)
	cancel1()
	cancel2()

	// Drain both goroutines; context.Canceled is expected.
	for i := 0; i < 2; i++ {
		err := <-errs
		if err != nil {
			// context cancellation causes an error on the client side — that's fine.
			assert.Contains(t, err.Error(), "context canceled")
		}
	}

	// Both consumers must have been registered with distinct IDs.
	// Use the mutex-safe getter to avoid a data race with the handler goroutines.
	registered := fr.Registered()
	assert.Len(t, registered, 2)
	assert.Contains(t, registered, "sse:consumer-a")
	assert.Contains(t, registered, "sse:consumer-b")
}

// Test 6 (resume integration): cursor store -> LoadCursor -> Router -> Deliver pipeline.
// Validates that a reconnecting consumer resumes from the persisted cursor position.
func TestSSEConsumer_CursorResume(t *testing.T) {
	db := newInMemoryDB(t)
	// Use a very short flush interval so the cursor reaches SQLite quickly.
	cs, err := checkpoint.NewSQLiteCursorStore(db, 5*time.Millisecond)
	require.NoError(t, err)
	ctx := context.Background()

	// Save a cursor at seq=42 for the consumer that will reconnect.
	const consumerID = "resume-test"
	const fullID = "sse:resume-test"
	err = cs.SaveCursor(ctx, fullID, 0, 42)
	require.NoError(t, err)

	// Wait for flush interval to pass so the dirty entry reaches SQLite.
	time.Sleep(20 * time.Millisecond)

	// Build a routerStub that mimics Router.Register: calls LoadCursor, then delivers
	// a LogEntry with Seq set to the loaded value.
	var deliveredSeq uint64
	routerStub := &loadCursorRouterStub{
		cs:              cs,
		onDeliveredSeq:  func(seq uint64) { deliveredSeq = seq },
		testEvent: &event.ChangeEvent{
			ID:             ulid.Make(),
			IdempotencyKey: "test:orders:1:insert:0/0",
			Operation:      event.OpInsert,
			Table:          "orders",
		},
	}

	// Create an SSEConsumer with the same consumerID.
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil) // allow all
	consumer := NewSSEConsumer(consumerID, rr, filter, cs, nil, nil, nil)

	// Register triggers LoadCursor -> Deliver with Seq=42.
	routerStub.Register(consumer)

	// Assert the event was delivered at seq=42 (the persisted cursor), not seq=1.
	assert.Equal(t, uint64(42), deliveredSeq,
		"consumer should resume from persisted cursor seq=42, not from seq=1")
}

// loadCursorRouterStub mimics Router.Register's cursor-loading behavior.
type loadCursorRouterStub struct {
	cs             router.ConsumerCursorStore
	onDeliveredSeq func(uint64)
	testEvent      *event.ChangeEvent
}

func (s *loadCursorRouterStub) Register(c router.Consumer) {
	ctx := context.Background()
	seq, err := s.cs.LoadCursor(ctx, c.ID(), 0)
	if err != nil {
		seq = 1
	}
	entry := eventlog.LogEntry{Seq: seq, Event: s.testEvent}
	s.onDeliveredSeq(entry.Seq)
	_ = c.Deliver(ctx, entry)
}
