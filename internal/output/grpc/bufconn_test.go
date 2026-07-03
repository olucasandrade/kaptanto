package grpcoutput

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/output/grpc/proto"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// stubJSONCodec is a test-only gRPC codec. The hand-written proto stubs in
// ./proto (cdc.pb.go, cdc_grpc.pb.go — generated without protoc being
// available, see their header comments) are plain Go structs that do not
// implement proto.Message, so grpc-go's real default "proto" codec cannot
// marshal them. Registering this JSON-based codec under the same name
// ("proto") lets a real *grpc.Server / *grpc.ClientConn pair exercise the
// actual wire path (framing, interceptors, stream lifecycle) end-to-end
// without depending on protoc/protobuf codegen being available in this
// environment. encoding.RegisterCodec (legacy V1) takes precedence over the
// real codec's V2 registration inside grpc-go's getCodec lookup, and this
// registration is scoped to this test binary process only — no production
// code is touched.
type stubJSONCodec struct{}

func (stubJSONCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (stubJSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func (stubJSONCodec) Name() string                       { return "proto" }

func init() {
	encoding.RegisterCodec(stubJSONCodec{})
}

// cdcStreamClient is a minimal hand-written client for proto.CdcStreamClient.
// The generated stub (cdc_grpc.pb.go) only defines the server side plus the
// client interface — protoc-gen-go-grpc was not available when it was
// hand-written (see its header comment), so no client implementation exists
// anywhere in the repo. This is the smallest correct implementation: it
// drives the same method names the real server's ServiceDesc registers
// ("/kaptanto.v1.CdcStream/Subscribe", ".../Acknowledge").
type cdcStreamClient struct {
	cc *grpc.ClientConn
}

func newCdcStreamClient(cc *grpc.ClientConn) proto.CdcStreamClient {
	return &cdcStreamClient{cc: cc}
}

func (c *cdcStreamClient) Subscribe(ctx context.Context, in *proto.SubscribeRequest, opts ...grpc.CallOption) (proto.CdcStream_SubscribeClient, error) {
	stream, err := c.cc.NewStream(ctx, &grpc.StreamDesc{StreamName: "Subscribe", ServerStreams: true},
		"/kaptanto.v1.CdcStream/Subscribe", opts...)
	if err != nil {
		return nil, err
	}
	x := &cdcStreamSubscribeClientImpl{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

func (c *cdcStreamClient) Acknowledge(ctx context.Context, in *proto.AcknowledgeRequest, opts ...grpc.CallOption) (*proto.AcknowledgeResponse, error) {
	out := new(proto.AcknowledgeResponse)
	if err := c.cc.Invoke(ctx, "/kaptanto.v1.CdcStream/Acknowledge", in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

type cdcStreamSubscribeClientImpl struct {
	grpc.ClientStream
}

func (x *cdcStreamSubscribeClientImpl) Recv() (*proto.ChangeEvent, error) {
	m := new(proto.ChangeEvent)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// These tests exercise the real GRPCServer.Subscribe/Acknowledge RPCs and the
// NewGRPCNetServer(WithAuth) constructors over an in-memory bufconn listener
// with a real generated grpc client — as opposed to consumer_test.go's tests,
// which only drive the channel-reading loop directly and never call the
// constructors or the actual Subscribe method.

// fakeGRPCEventLog is a minimal eventlog.EventLog fake that serves
// pre-seeded LogEntry slices per partition, mirroring router_test.go's
// fakeEventLog (kept package-local to avoid a cross-package test dependency).
type fakeGRPCEventLog struct {
	mu   sync.Mutex
	data map[uint32][]eventlog.LogEntry
}

func newFakeGRPCEventLog(data map[uint32][]eventlog.LogEntry) *fakeGRPCEventLog {
	return &fakeGRPCEventLog{data: data}
}

func (f *fakeGRPCEventLog) Append(_ *event.ChangeEvent) (uint64, error) { return 0, nil }

func (f *fakeGRPCEventLog) ReadPartition(_ context.Context, partition uint32, fromSeq uint64, limit int) ([]eventlog.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []eventlog.LogEntry
	for _, e := range f.data[partition] {
		if e.Seq >= fromSeq {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeGRPCEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	return make([]uint64, len(evs)), nil
}

func (f *fakeGRPCEventLog) Close() error { return nil }

// seed adds entries to a partition after construction, under lock. Used to
// populate data only after a consumer has registered with the Router,
// avoiding a tight busy-poll loop (0 consumers, non-empty ReadPartition never
// waits) between Run() starting and Subscribe() registering the consumer.
func (f *fakeGRPCEventLog) seed(partition uint32, entries []eventlog.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = make(map[uint32][]eventlog.LogEntry)
	}
	f.data[partition] = append(f.data[partition], entries...)
}

// grpcTestEvent builds a minimal ChangeEvent for a given seq/table.
func grpcTestEvent(seq uint64, table string) *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             ulid.Make(),
		IdempotencyKey: "test:" + table + ":insert",
		Operation:      event.OpInsert,
		Table:          table,
		After:          json.RawMessage(`{"id":1}`),
	}
}

// startBufconnServer registers gs on a real *grpc.Server served over an
// in-memory bufconn listener and returns a dialer usable with
// grpc.WithContextDialer, plus a cleanup func.
func startBufconnServer(t *testing.T, srv *grpc.Server) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

// dialBufconn creates a grpc.ClientConn against the bufconn dialer.
func dialBufconn(t *testing.T, dialer func(context.Context, string) (net.Conn, error)) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestGRPCServer_Bufconn_SubscribeStreamsEventsInSeqOrder verifies that the
// real Subscribe RPC (not just the channel-reading loop) delivers events read
// from the EventLog to a real gRPC client, in ascending seq order.
func TestGRPCServer_Bufconn_SubscribeStreamsEventsInSeqOrder(t *testing.T) {
	entries := []eventlog.LogEntry{
		{Seq: 1, Event: grpcTestEvent(1, "orders")},
		{Seq: 2, Event: grpcTestEvent(2, "orders")},
		{Seq: 3, Event: grpcTestEvent(3, "orders")},
	}
	// Start with an empty log: entries are seeded only after Subscribe has
	// registered a consumer (see below). Seeding before registration would
	// make the router's per-partition loop spin on a non-empty ReadPartition
	// batch with zero consumers to deliver to, racing the client's dial.
	el := newFakeGRPCEventLog(nil)
	cs := router.NewNoopCursorStore()
	r := router.NewRouter(el, 1, cs)

	gs := NewGRPCServer(r, cs, nil, nil, nil)
	grpcSrv := NewGRPCNetServer(gs, nil)
	dialer := startBufconnServer(t, grpcSrv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = r.Run(ctx) }()

	conn := dialBufconn(t, dialer)
	client := newCdcStreamClient(conn)

	stream, err := client.Subscribe(ctx, &proto.SubscribeRequest{ConsumerId: "bufconn-consumer"})
	require.NoError(t, err)

	// Give the server-side handler time to call Router.Register before
	// seeding events, so the router's first non-empty read has a consumer
	// to deliver to.
	time.Sleep(100 * time.Millisecond)
	el.seed(0, entries)

	var gotIDs []string
	for i := 0; i < 3; i++ {
		ev, err := stream.Recv()
		require.NoError(t, err, "Recv() %d", i)
		gotIDs = append(gotIDs, ev.Id)
		assert.Equal(t, "orders", ev.Table)
		assert.NotEmpty(t, ev.Payload)
	}

	require.Len(t, gotIDs, 3)
	assert.Equal(t, entries[0].Event.ID.String(), gotIDs[0], "first delivered event must be seq=1")
	assert.Equal(t, entries[1].Event.ID.String(), gotIDs[1], "second delivered event must be seq=2")
	assert.Equal(t, entries[2].Event.ID.String(), gotIDs[2], "third delivered event must be seq=3")
}

// TestGRPCServer_Bufconn_AcknowledgeAdvancesSharedCursorStore verifies that
// the Acknowledge RPC, called over the real wire protocol, persists a cursor
// on the exact same ConsumerCursorStore instance the Router reads from —
// mirroring how cmd/root.go wires GRPCServer and Router to the same store.
func TestGRPCServer_Bufconn_AcknowledgeAdvancesSharedCursorStore(t *testing.T) {
	el := newFakeGRPCEventLog(nil)
	cs := router.NewNoopCursorStore()
	r := router.NewRouter(el, 1, cs)

	gs := NewGRPCServer(r, cs, nil, nil, nil)
	grpcSrv := NewGRPCNetServer(gs, nil)
	dialer := startBufconnServer(t, grpcSrv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialBufconn(t, dialer)
	client := newCdcStreamClient(conn)

	resp, err := client.Acknowledge(ctx, &proto.AcknowledgeRequest{
		ConsumerId:  "ack-consumer",
		PartitionId: 0,
		Seq:         42,
	})
	require.NoError(t, err)
	assert.True(t, resp.Ok)

	seq, err := cs.LoadCursor(ctx, "grpc:ack-consumer", 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), seq, "Acknowledge must persist the cursor on the store shared with the Router")
}

// TestGRPCServer_Bufconn_AuthRejectsMissingToken verifies that
// NewGRPCNetServerWithAuth enforces the bearer token on both RPCs: a call
// without an "authorization" header is rejected with Unauthenticated.
func TestGRPCServer_Bufconn_AuthRejectsMissingToken(t *testing.T) {
	el := newFakeGRPCEventLog(nil)
	cs := router.NewNoopCursorStore()
	r := router.NewRouter(el, 1, cs)

	gs := NewGRPCServer(r, cs, nil, nil, nil)
	grpcSrv := NewGRPCNetServerWithAuth(gs, "s3cr3t-token", nil)
	dialer := startBufconnServer(t, grpcSrv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialBufconn(t, dialer)
	client := newCdcStreamClient(conn)

	_, err := client.Acknowledge(ctx, &proto.AcknowledgeRequest{ConsumerId: "x", PartitionId: 0, Seq: 1})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// Streaming RPC: the interceptor runs before Subscribe's loop, so the
	// rejection surfaces on Recv() rather than on Subscribe() itself.
	stream, err := client.Subscribe(ctx, &proto.SubscribeRequest{ConsumerId: "x"})
	require.NoError(t, err) // client-side stream creation never fails locally
	_, recvErr := stream.Recv()
	require.Error(t, recvErr)
	assert.Equal(t, codes.Unauthenticated, status.Code(recvErr))
}

// TestGRPCServer_Bufconn_AuthAcceptsValidToken verifies the mirror-image
// success path: a call carrying the correct bearer token is accepted.
func TestGRPCServer_Bufconn_AuthAcceptsValidToken(t *testing.T) {
	el := newFakeGRPCEventLog(nil)
	cs := router.NewNoopCursorStore()
	r := router.NewRouter(el, 1, cs)

	gs := NewGRPCServer(r, cs, nil, nil, nil)
	grpcSrv := NewGRPCNetServerWithAuth(gs, "s3cr3t-token", nil)
	dialer := startBufconnServer(t, grpcSrv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer s3cr3t-token")

	conn := dialBufconn(t, dialer)
	client := newCdcStreamClient(conn)

	resp, err := client.Acknowledge(ctx, &proto.AcknowledgeRequest{ConsumerId: "y", PartitionId: 0, Seq: 7})
	require.NoError(t, err)
	assert.True(t, resp.Ok)
}

// TestGRPCServer_Bufconn_ClientCancelUnblocksSubscribeCleanly verifies that
// Subscribe's select loop exits without a panic when the *client* cancels its
// own stream context mid-stream (e.g. the client process disconnects). This
// is the exit path Subscribe actually implements: `case <-ctx.Done(): return
// status.FromContextError(ctx.Err()).Err()` where ctx is stream.Context().
func TestGRPCServer_Bufconn_ClientCancelUnblocksSubscribeCleanly(t *testing.T) {
	el := newFakeGRPCEventLog(nil) // no data: stream stays open, blocked on ctx/consumer.ch
	cs := router.NewNoopCursorStore()
	r := router.NewRouter(el, 1, cs)

	gs := NewGRPCServer(r, cs, nil, nil, nil)
	grpcSrv := NewGRPCNetServer(gs, nil)
	dialer := startBufconnServer(t, grpcSrv)

	routerCtx, routerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer routerCancel()
	go func() { _ = r.Run(routerCtx) }()

	conn := dialBufconn(t, dialer)
	client := newCdcStreamClient(conn)

	streamCtx, streamCancel := context.WithCancel(context.Background())

	stream, err := client.Subscribe(streamCtx, &proto.SubscribeRequest{ConsumerId: "cancel-test"})
	require.NoError(t, err)

	// Give the server a moment to register the consumer, then simulate the
	// client disconnecting.
	time.Sleep(50 * time.Millisecond)
	streamCancel()

	recvDone := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvDone <- err
	}()
	select {
	case err := <-recvDone:
		require.Error(t, err, "Recv() must return a non-panic error once the client cancels")
		assert.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(4 * time.Second):
		t.Fatal("stream.Recv() did not return after client-side cancellation")
	}

	// GracefulStop must return promptly once there is no active stream left.
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NotPanics(t, func() { grpcSrv.GracefulStop() })
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("GracefulStop did not return after the only stream was closed")
	}
}

// NOTE ON A DISCOVERED GAP (not fixed here — see PR description):
// GRPCServer.Subscribe's only exit paths are stream.Context().Done() (client
// disconnect) or consumer.ch being closed (which never happens — nothing
// ever closes it). Server-initiated shutdown in cmd/internal/output.go calls
// grpcSrv.GracefulStop() directly on <-ctx.Done(), with no fallback Stop()/
// deadline. grpc.Server.GracefulStop() does not cancel in-flight stream
// contexts; it only waits for active RPCs to finish on their own. If a
// Subscribe client is connected and does not disconnect on its own,
// GracefulStop() blocks forever, so process shutdown hangs. Verified locally
// with a variant of this test that opens a stream and never cancels it before
// calling GracefulStop() — it does not return within any bounded timeout.
// This is a production shutdown-liveness bug, not a test gap; fixing it needs
// a design decision (e.g. a shutdown context threaded into Subscribe's select,
// or wrapping GracefulStop with a bounded Stop() fallback in output.go) that
// is out of scope for this test-only fix plan.
