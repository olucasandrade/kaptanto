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
	id         string // stable: "sse:<consumerID>"
	w          http.ResponseWriter
	rc         *http.ResponseController
	filter     *output.EventFilter
	cs         router.ConsumerCursorStore
	m          *observability.KaptantoMetrics
	rowFilters map[string]*output.RowFilter // CFG-06: per-table WHERE-expression filter; nil map = pass-through
	colFilters map[string][]string          // CFG-05: per-table column allow-list; nil map = pass-through
}

// Compile-time assertion: SSEConsumer implements router.Consumer.
var _ router.Consumer = (*SSEConsumer)(nil)

// NewSSEConsumer constructs an SSEConsumer for the given HTTP connection.
// rowFilters and colFilters are per-table maps; nil maps are treated as
// pass-through (equivalent to no filter configured).
func NewSSEConsumer(
	consumerID string,
	w http.ResponseWriter,
	filter *output.EventFilter,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
) *SSEConsumer {
	return &SSEConsumer{
		id:         "sse:" + consumerID,
		w:          w,
		rc:         http.NewResponseController(w),
		filter:     filter,
		cs:         cs,
		m:          m,
		rowFilters: rowFilters,
		colFilters: colFilters,
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

	// Row filter (CFG-06): look up per-table filter by event table name.
	// If a filter is configured for this table and the event does not match,
	// advance cursor silently without writing to the wire.
	if rf, ok := c.rowFilters[entry.Event.Table]; ok && rf != nil {
		if !rf.Match(entry.Event) {
			return c.cs.SaveCursor(ctx, c.id, entry.PartitionID, entry.Seq)
		}
	}

	// Column filter (CFG-05): look up per-table allowed columns by event table name.
	// ApplyColumnFilter is a no-op when cols is nil/empty.
	// IMPORTANT: entry.Event is a shared pointer — copy into a new struct, never mutate.
	ev := entry.Event
	cols := c.colFilters[entry.Event.Table] // nil if table not configured
	filteredBefore, err := output.ApplyColumnFilter(ev.Before, cols)
	if err != nil {
		return fmt.Errorf("sse: column filter before: %w", err)
	}
	filteredAfter, err := output.ApplyColumnFilter(ev.After, cols)
	if err != nil {
		return fmt.Errorf("sse: column filter after: %w", err)
	}
	// Build a filtered event value (shallow copy; only Before/After differ).
	filtered := *ev
	filtered.Before = filteredBefore
	filtered.After = filteredAfter

	// SSE wire format: id line + data line (JSON-encoded) + blank line terminator.
	if _, err := fmt.Fprintf(c.w, "id: %s\ndata: ", entry.Event.ID.String()); err != nil {
		if c.m != nil {
			c.m.ErrorsTotal.WithLabelValues(c.id, "deliver").Inc()
		}
		return err // broken pipe -> isPermanentError -> dead-letter
	}
	if err := json.NewEncoder(c.w).Encode(&filtered); err != nil {
		if c.m != nil {
			c.m.ErrorsTotal.WithLabelValues(c.id, "deliver").Inc()
		}
		return err
	}
	if _, err := fmt.Fprint(c.w, "\n"); err != nil {
		if c.m != nil {
			c.m.ErrorsTotal.WithLabelValues(c.id, "deliver").Inc()
		}
		return err
	}
	if err := c.rc.Flush(); err != nil {
		if c.m != nil {
			c.m.ErrorsTotal.WithLabelValues(c.id, "deliver").Inc()
		}
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
