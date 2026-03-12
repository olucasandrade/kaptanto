// Package grpcoutput implements the gRPC output consumer and server for kaptanto.
// The package name is grpcoutput to avoid collision with the grpc import alias.
package grpcoutput

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/observability"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/output/grpc/proto"
	"github.com/kaptanto/kaptanto/internal/router"
)

// GRPCConsumer implements router.Consumer via a channel bridge.
//
// Deliver sends to a buffered channel (non-blocking). The Subscribe goroutine
// reads from the channel and calls stream.Send() OUTSIDE any Router or
// RetryScheduler lock — preventing deadlock from HTTP/2 backpressure (OUT-08).
type GRPCConsumer struct {
	id     string // stable: "grpc:<consumerID>"
	ch     chan *proto.ChangeEvent
	filter *output.EventFilter
	cs     router.ConsumerCursorStore
	m      *observability.KaptantoMetrics
	done   chan struct{} // closed when Subscribe handler exits
}

// Compile-time assertion: GRPCConsumer implements router.Consumer.
var _ router.Consumer = (*GRPCConsumer)(nil)

// NewGRPCConsumer constructs a GRPCConsumer.
// bufSize controls the channel depth; 64 is the recommended default.
func NewGRPCConsumer(
	consumerID string,
	bufSize int,
	filter *output.EventFilter,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
) *GRPCConsumer {
	return &GRPCConsumer{
		id:     "grpc:" + consumerID,
		ch:     make(chan *proto.ChangeEvent, bufSize),
		filter: filter,
		cs:     cs,
		m:      m,
		done:   make(chan struct{}),
	}
}

// ID returns the stable consumer identifier used for cursor persistence.
func (c *GRPCConsumer) ID() string { return c.id }

// Deliver encodes the event to a proto ChangeEvent and sends to the buffered
// channel. The send is non-blocking: if the channel is full (slow client),
// an error is returned so the RetryScheduler backs off.
//
// stream.Send() is called by the Subscribe goroutine OUTSIDE any lock,
// so HTTP/2 backpressure cannot deadlock the Router dispatch loop (OUT-08).
func (c *GRPCConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	if !c.filter.Allow(entry.Event) {
		return nil // filtered: cursor advances via Router
	}

	// Encode payload as full JSON for OUT-07 (JSON fallback).
	payload, err := json.Marshal(entry.Event)
	if err != nil {
		return fmt.Errorf("grpc consumer: marshal event: %w", err)
	}

	protoEv := &proto.ChangeEvent{
		Id:             entry.Event.ID.String(),
		IdempotencyKey: entry.Event.IdempotencyKey,
		Operation:      string(entry.Event.Operation),
		Table:          entry.Event.Table,
		Payload:        payload,
	}

	// Non-blocking send: if channel is full return error so RetryScheduler backs off.
	select {
	case c.ch <- protoEv:
		if c.m != nil {
			c.m.EventsDelivered.WithLabelValues(c.id, entry.Event.Table, string(entry.Event.Operation)).Inc()
		}
		return nil
	case <-c.done:
		return fmt.Errorf("grpc consumer: subscribe handler exited")
	default:
		return fmt.Errorf("grpc consumer: channel full, slow client (backpressure)")
	}
}

// Close signals that the Subscribe handler has exited.
// Called by GRPCServer.Subscribe with defer before returning.
func (c *GRPCConsumer) Close() {
	close(c.done)
}
