package sse

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaptanto/kaptanto/internal/observability"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/router"
)

// SSEServer is an http.Handler that manages SSE connections.
// Each GET request creates an independent SSEConsumer registered with the Router.
// Headers are set before router.Register is called to ensure the first Deliver
// call does not trigger a wrong Content-Type header flush (SSE pitfall #1).
type SSEServer struct {
	router       *router.Router
	cursorStore  router.ConsumerCursorStore
	metrics      *observability.KaptantoMetrics
	corsOrigin   string        // e.g. "*" or "https://example.com"
	pingInterval time.Duration // keepalive comment period; default 15s
	rowFilters   map[string]*output.RowFilter // CFG-06: per-table row filter; nil = pass-through for all tables
	colFilters   map[string][]string          // CFG-05: per-table column allow-list; nil = pass-through for all tables
}

// NewSSEServer constructs an SSEServer.
// corsOrigin defaults to "*" if empty. pingInterval defaults to 15s if zero.
// rowFilters and colFilters are per-table maps; nil maps are treated as
// pass-through (equivalent to no filter configured for any table).
func NewSSEServer(
	r *router.Router,
	cs router.ConsumerCursorStore,
	m *observability.KaptantoMetrics,
	corsOrigin string,
	pingInterval time.Duration,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
) *SSEServer {
	if pingInterval == 0 {
		pingInterval = 15 * time.Second
	}
	if corsOrigin == "" {
		corsOrigin = "*"
	}
	return &SSEServer{
		router:       r,
		cursorStore:  cs,
		metrics:      m,
		corsOrigin:   corsOrigin,
		pingInterval: pingInterval,
		rowFilters:   rowFilters,
		colFilters:   colFilters,
	}
}

// ServeHTTP handles a single SSE connection.
//
// Query parameters:
//   - consumer: stable consumer ID (falls back to RemoteAddr if absent)
//   - tables: comma-separated table allow-list (empty = all)
//   - operations: comma-separated operation allow-list (empty = all)
//
// Headers:
//   - Last-Event-ID: if present, consumer ID drives cursor lookup via the
//     cursor store — the Router calls LoadCursor(consumer.ID()) on Register.
func (s *SSEServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers BEFORE any write (first write flushes headers implicitly).
	// CRITICAL: must happen before router.Register which triggers immediate Deliver.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)

	consumerID := r.URL.Query().Get("consumer")
	if consumerID == "" {
		consumerID = r.RemoteAddr
	}

	// Parse filter from query params: ?tables=orders,payments&operations=insert,update
	tables := filterNonEmpty(strings.Split(r.URL.Query().Get("tables"), ","))
	ops := filterNonEmpty(strings.Split(r.URL.Query().Get("operations"), ","))
	filter := output.NewEventFilter(tables, ops)

	consumer := NewSSEConsumer(consumerID, w, filter, s.cursorStore, s.metrics, s.rowFilters, s.colFilters)

	// Last-Event-ID: consumerID is the resume key. The cursor store holds the
	// persisted (partitionID, seq) from the prior connection's SaveCursor calls.
	// The Router's runPartition loop calls cursorStore.LoadCursor(consumer.ID())
	// when Register is invoked — no direct action needed here.
	lastEventID := r.Header.Get("Last-Event-ID")
	_ = lastEventID // cursor loaded by Router via consumer.ID()

	s.router.Register(consumer)
	// Close must be called before ServeHTTP returns. This blocks until any
	// in-flight Deliver completes, preventing use of the ResponseWriter after
	// net/http has torn it down (nil-deref panic in rc.Flush).
	defer consumer.Close()

	pingTicker := time.NewTicker(s.pingInterval)
	defer pingTicker.Stop()

	rc := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingTicker.C:
			fmt.Fprint(w, ": ping\n\n")
			rc.Flush() // ignore error; next Deliver will surface the broken pipe
		}
	}
}

// filterNonEmpty removes empty strings from a slice.
func filterNonEmpty(ss []string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
