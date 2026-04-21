package grpcoutput

import (
	"context"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/output"
	"github.com/olucasandrade/kaptanto/internal/output/grpc/proto"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// GRPCServer implements proto.CdcStreamServer.
// It manages gRPC Subscribe streams and the Acknowledge RPC.
type GRPCServer struct {
	proto.UnimplementedCdcStreamServer
	router      *router.Router
	cursorStore router.ConsumerCursorStore
	metrics     *observability.KaptantoMetrics
	rowFilters  map[string]*output.RowFilter // CFG-06: per-table row filter; nil = pass-through for all tables
	colFilters  map[string][]string          // CFG-05: per-table column allow-list; nil = pass-through for all tables
}

// NewGRPCServer constructs a GRPCServer.
// rowFilters and colFilters are per-table maps; nil maps are treated as
// pass-through (equivalent to no filter configured for any table).
func NewGRPCServer(
	r *router.Router,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
) *GRPCServer {
	return &GRPCServer{router: r, cursorStore: cs, metrics: m, rowFilters: rowFilters, colFilters: colFilters}
}

// NewGRPCNetServer creates and configures the grpc.Server.
// Call Serve(lis) on the returned server to start accepting connections.
func NewGRPCNetServer(svc *GRPCServer) *grpclib.Server {
	srv := grpclib.NewServer(
		grpclib.MaxConcurrentStreams(1000),
		grpclib.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	)
	proto.RegisterCdcStreamServer(srv, svc)
	return srv
}

// Subscribe is the server-streaming RPC. It creates a GRPCConsumer, registers
// it with the Router, then forwards events from the consumer's channel to the
// gRPC stream. stream.Send() is called here, OUTSIDE the Router lock, so
// HTTP/2 backpressure cannot deadlock the dispatch loop (OUT-08).
func (s *GRPCServer) Subscribe(req *proto.SubscribeRequest, stream proto.CdcStream_SubscribeServer) error {
	filter := output.NewEventFilter(req.Tables, req.Operations)
	consumer := NewGRPCConsumer(req.ConsumerId, 64, filter, s.cursorStore, s.metrics, s.rowFilters, s.colFilters)
	defer consumer.Close() // signals Deliver that handler exited

	s.router.Register(consumer)

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case ev, ok := <-consumer.ch:
			if !ok {
				return nil
			}
			// stream.Send blocks when HTTP/2 window is exhausted (OUT-08).
			// Called here OUTSIDE any Router or RetryScheduler lock.
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// Acknowledge is the unary RPC that advances the consumer's durable cursor.
// Clients call this after successfully processing an event.
func (s *GRPCServer) Acknowledge(ctx context.Context, req *proto.AcknowledgeRequest) (*proto.AcknowledgeResponse, error) {
	consumerID := "grpc:" + req.ConsumerId
	if err := s.cursorStore.SaveCursor(ctx, consumerID, req.PartitionId, req.Seq); err != nil {
		return nil, status.Errorf(codes.Internal, "save cursor: %v", err)
	}
	return &proto.AcknowledgeResponse{Ok: true}, nil
}
