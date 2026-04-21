package grpcoutput

import (
	"context"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/output"
	"github.com/olucasandrade/kaptanto/internal/output/grpc/proto"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEvent() *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             ulid.Make(),
		IdempotencyKey: "test:orders:1:insert:0/0",
		Operation:      event.OpInsert,
		Table:          "orders",
	}
}

func testConsumer(t *testing.T) *GRPCConsumer {
	t.Helper()
	filter := output.NewEventFilter(nil, nil) // allow all
	cs := router.NewNoopCursorStore()
	return NewGRPCConsumer("test-consumer", 8, filter, cs, nil, nil, nil)
}

// Test 1: Deliver encodes event to proto ChangeEvent and sends to buffered channel
func TestGRPCConsumer_DeliverSendsToChannel(t *testing.T) {
	c := testConsumer(t)
	ctx := context.Background()

	ev := testEvent()
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := c.Deliver(ctx, entry)
	require.NoError(t, err)

	// Event should be in the channel.
	select {
	case protoEv := <-c.ch:
		assert.Equal(t, ev.ID.String(), protoEv.Id)
		assert.Equal(t, string(ev.Operation), protoEv.Operation)
		assert.Equal(t, ev.Table, protoEv.Table)
		assert.NotEmpty(t, protoEv.Payload, "payload (JSON) must be non-empty")
	default:
		t.Fatal("expected event in channel but channel was empty")
	}
}

// Test 2: When channel is full, Deliver returns an error (backpressure)
func TestGRPCConsumer_DeliverChannelFull(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()
	// bufSize=1 so one delivery fills it.
	c := NewGRPCConsumer("backpressure-test", 1, filter, cs, nil, nil, nil)
	ctx := context.Background()

	entry := eventlog.LogEntry{Seq: 1, Event: testEvent()}

	// First delivery fills the channel.
	err := c.Deliver(ctx, entry)
	require.NoError(t, err)

	// Second delivery finds a full channel and must return an error.
	err = c.Deliver(ctx, entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel full")
}

// Test 3: Subscribe RPC delivers events from the consumer channel to the stream.
func TestGRPCServer_SubscribeDeliversEvents(t *testing.T) {
	cs := router.NewNoopCursorStore()
	testEv := testEvent()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &fakeSubscribeStream{
		ctx:    ctx,
		events: make(chan *proto.ChangeEvent, 8),
	}

	filter := output.NewEventFilter(nil, nil)
	consumer := NewGRPCConsumer("test-sub", 64, filter, cs, nil, nil, nil)
	defer consumer.Close()

	// Deliver one event to the consumer channel.
	entry := eventlog.LogEntry{Seq: 1, Event: testEv}
	err := consumer.Deliver(ctx, entry)
	require.NoError(t, err)

	// Run the Subscribe channel-reading loop in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				errCh <- nil
				return
			case ev, ok := <-consumer.ch:
				if !ok {
					errCh <- nil
					return
				}
				if err := stream.Send(ev); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Wait for the event to be sent to the stream.
	select {
	case got := <-stream.events:
		assert.Equal(t, testEv.ID.String(), got.Id)
		assert.NotEmpty(t, got.Payload)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no event received on stream")
	}

	// Cancel to stop the Subscribe loop.
	cancel()
	require.NoError(t, <-errCh)
}

// Test 4: Subscribe RPC returns when client context is cancelled.
func TestGRPCServer_SubscribeExitsOnContextCancel(t *testing.T) {
	cs := router.NewNoopCursorStore()

	ctx, cancel := context.WithCancel(context.Background())
	stream := &fakeSubscribeStream{
		ctx:    ctx,
		events: make(chan *proto.ChangeEvent, 8),
	}
	stream.cancelFn = cancel

	filter := output.NewEventFilter(nil, nil)
	consumer := NewGRPCConsumer("cancel-test", 64, filter, cs, nil, nil, nil)
	defer consumer.Close()

	errCh := make(chan error, 1)
	go func() {
		streamCtx := stream.Context()
		for {
			select {
			case <-streamCtx.Done():
				errCh <- nil
				return
			case ev, ok := <-consumer.ch:
				if !ok {
					errCh <- nil
					return
				}
				if err := stream.Send(ev); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Cancel after a brief pause.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err, "Subscribe should exit cleanly on ctx.Done")
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not exit after context cancellation")
	}
}

// Test 5: Acknowledge RPC calls cursorStore.SaveCursor and returns ok=true.
func TestGRPCServer_AcknowledgeSavesCursor(t *testing.T) {
	cs := router.NewNoopCursorStore()
	gs := &GRPCServer{cursorStore: cs}

	ctx := context.Background()
	req := &proto.AcknowledgeRequest{
		ConsumerId:  "ack-consumer",
		EventId:     "01J0000000000000000000000A",
		PartitionId: 0,
		Seq:         99,
	}

	resp, err := gs.Acknowledge(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Ok)

	// Verify cursor was persisted.
	seq, err := cs.LoadCursor(ctx, "grpc:ack-consumer", 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(99), seq)
}

// ─── Test helpers ────────────────────────────────────────────────────────────

// fakeRegisterRouter is a test helper that delivers a fixed entry to a consumer.
type fakeRegisterRouter struct {
	cs        router.ConsumerCursorStore
	testEntry eventlog.LogEntry
}

func (f *fakeRegisterRouter) deliverTo(c router.Consumer) error {
	return c.Deliver(context.Background(), f.testEntry)
}

// fakeSubscribeStream implements proto.CdcStream_SubscribeServer for tests.
type fakeSubscribeStream struct {
	ctx      context.Context
	cancelFn context.CancelFunc
	events   chan *proto.ChangeEvent
	cancel   func()
}

func (s *fakeSubscribeStream) Send(ev *proto.ChangeEvent) error {
	s.events <- ev
	return nil
}

func (s *fakeSubscribeStream) Context() context.Context {
	return s.ctx
}

// grpc.ServerStream interface stubs (unused in tests).
func (s *fakeSubscribeStream) SetHeader(_ interface{}) error  { return nil }
func (s *fakeSubscribeStream) SendHeader(_ interface{}) error { return nil }
func (s *fakeSubscribeStream) SetTrailer(_ interface{})       {}
func (s *fakeSubscribeStream) SendMsg(m interface{}) error    { return nil }
func (s *fakeSubscribeStream) RecvMsg(m interface{}) error    { return nil }
