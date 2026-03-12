// Package sse implements the SSE (Server-Sent Events) output consumer and HTTP
// server for kaptanto. SSEConsumer implements router.Consumer for a single
// HTTP connection; SSEServer is the http.Handler that manages connections.
package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/observability"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/router"
)

// SSEConsumer implements router.Consumer for a single SSE connection.
// Created per-connection in SSEServer.ServeHTTP.
//
// Lifecycle: Register with router -> Deliver events until client disconnects
// -> Deliver returns a write error -> RetryScheduler dead-letters the consumer
// (isPermanentError path). No explicit Deregister needed.
type SSEConsumer struct {
	id     string // stable: "sse:<consumerID>"
	w      http.ResponseWriter
	rc     *http.ResponseController
	filter *output.EventFilter
	cs     router.ConsumerCursorStore
	m      *observability.KaptantoMetrics
}

// Compile-time assertion: SSEConsumer implements router.Consumer.
var _ router.Consumer = (*SSEConsumer)(nil)

// NewSSEConsumer constructs an SSEConsumer for the given HTTP connection.
func NewSSEConsumer(
	consumerID string,
	w http.ResponseWriter,
	filter *output.EventFilter,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
) *SSEConsumer {
	return &SSEConsumer{
		id:     "sse:" + consumerID,
		w:      w,
		rc:     http.NewResponseController(w),
		filter: filter,
		cs:     cs,
		m:      m,
	}
}

// ID returns the stable consumer identifier used for cursor persistence.
func (c *SSEConsumer) ID() string { return c.id }

// Deliver writes an event to the SSE stream in the wire format:
//
//	id: <ULID>\n
//	data: <JSON>\n
//	\n
//
// Filtered events silently advance the cursor without writing to the wire.
// Any write error is returned directly; a broken pipe / closed connection
// error is classified as permanent by the RetryScheduler (dead-letter path).
func (c *SSEConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	// Filtered events: advance cursor silently, no write to wire.
	if !c.filter.Allow(entry.Event) {
		// TODO: LogEntry.PartitionID not yet on struct; use 0 until added.
		return c.cs.SaveCursor(ctx, c.id, 0, entry.Seq)
	}

	// SSE wire format: id line + data line (JSON-encoded) + blank line terminator.
	if _, err := fmt.Fprintf(c.w, "id: %s\ndata: ", entry.Event.ID.String()); err != nil {
		return err // broken pipe -> isPermanentError -> dead-letter
	}
	if err := json.NewEncoder(c.w).Encode(entry.Event); err != nil {
		return err
	}
	if _, err := fmt.Fprint(c.w, "\n"); err != nil {
		return err
	}
	if err := c.rc.Flush(); err != nil {
		return err
	}

	// Persist cursor after successful delivery (seq+1 = resume from next event).
	// TODO: LogEntry.PartitionID not yet on struct; use 0 until added.
	if err := c.cs.SaveCursor(ctx, c.id, 0, entry.Seq+1); err != nil {
		return err
	}

	if c.m != nil {
		c.m.EventsDelivered.WithLabelValues(c.id, entry.Event.Table, string(entry.Event.Operation)).Inc()
	}
	return nil
}
