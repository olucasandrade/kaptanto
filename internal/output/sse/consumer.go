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
	id             string // stable: "sse:<consumerID>"
	w              http.ResponseWriter
	rc             *http.ResponseController
	filter         *output.EventFilter
	cs             router.ConsumerCursorStore
	m              *observability.KaptantoMetrics
	rowFilter      *output.RowFilter  // CFG-06: WHERE-expression filter; nil = pass-through
	allowedColumns []string           // CFG-05: column allow-list; nil/empty = pass-through
}

// Compile-time assertion: SSEConsumer implements router.Consumer.
var _ router.Consumer = (*SSEConsumer)(nil)

// NewSSEConsumer constructs an SSEConsumer for the given HTTP connection.
// rowFilter and allowedColumns may be nil/empty for pass-through behavior.
func NewSSEConsumer(
	consumerID string,
	w http.ResponseWriter,
	filter *output.EventFilter,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
	rowFilter *output.RowFilter,
	allowedColumns []string,
) *SSEConsumer {
	return &SSEConsumer{
		id:             "sse:" + consumerID,
		w:              w,
		rc:             http.NewResponseController(w),
		filter:         filter,
		cs:             cs,
		m:              m,
		rowFilter:      rowFilter,
		allowedColumns: allowedColumns,
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
		return c.cs.SaveCursor(ctx, c.id, entry.PartitionID, entry.Seq)
	}

	// Row filter (CFG-06): if a WHERE expression is configured and the event
	// does not match, advance cursor silently without writing to the wire.
	if c.rowFilter != nil && !c.rowFilter.Match(entry.Event) {
		return c.cs.SaveCursor(ctx, c.id, entry.PartitionID, entry.Seq)
	}

	// Column filter (CFG-05): strip disallowed columns from Before/After.
	// ApplyColumnFilter is a no-op when c.allowedColumns is nil/empty.
	// IMPORTANT: entry.Event is a shared pointer — copy into a new struct, never mutate.
	ev := entry.Event
	filteredBefore, err := output.ApplyColumnFilter(ev.Before, c.allowedColumns)
	if err != nil {
		return fmt.Errorf("sse: column filter before: %w", err)
	}
	filteredAfter, err := output.ApplyColumnFilter(ev.After, c.allowedColumns)
	if err != nil {
		return fmt.Errorf("sse: column filter after: %w", err)
	}
	// Build a filtered event value (shallow copy; only Before/After differ).
	filtered := *ev
	filtered.Before = filteredBefore
	filtered.After = filteredAfter

	// SSE wire format: id line + data line (JSON-encoded) + blank line terminator.
	if _, err := fmt.Fprintf(c.w, "id: %s\ndata: ", entry.Event.ID.String()); err != nil {
		return err // broken pipe -> isPermanentError -> dead-letter
	}
	if err := json.NewEncoder(c.w).Encode(&filtered); err != nil {
		return err
	}
	if _, err := fmt.Fprint(c.w, "\n"); err != nil {
		return err
	}
	if err := c.rc.Flush(); err != nil {
		return err
	}

	// Persist cursor after successful delivery (seq+1 = resume from next event).
	if err := c.cs.SaveCursor(ctx, c.id, entry.PartitionID, entry.Seq+1); err != nil {
		return err
	}

	if c.m != nil {
		c.m.EventsDelivered.WithLabelValues(c.id, entry.Event.Table, string(entry.Event.Operation)).Inc()
	}
	return nil
}
