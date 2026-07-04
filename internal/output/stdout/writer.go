// Package stdout provides a router.Consumer implementation that writes each
// delivered event as a single NDJSON line to an io.Writer (typically os.Stdout).
//
// StdoutWriter.Deliver is safe for concurrent use: the Router runs one
// goroutine per partition (see internal/router/router.go), and every
// partition goroutine can call Deliver on the SAME registered consumer
// instance concurrently — "sequentially per consumer" only holds within a
// single partition/message group, not across partitions. Two events landing
// on the same io.Writer at the same time without serialization would
// interleave mid-write and corrupt the NDJSON line framing (one JSON value
// per line), silently losing both events for any downstream line-based
// parser. mu below serializes each event's payload+newline as one atomic
// write sequence.
package stdout

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// StdoutWriter implements router.Consumer and writes events as NDJSON.
type StdoutWriter struct {
	mu sync.Mutex
	w  io.Writer
	m  *observability.KaptantoMetrics
}

// NewStdoutWriter creates a StdoutWriter that encodes events to w.
// Pass os.Stdout for production use; pass a bytes.Buffer for testing.
func NewStdoutWriter(w io.Writer) *StdoutWriter {
	return &StdoutWriter{w: w}
}

// SetMetrics wires the shared KaptantoMetrics instance post-construction.
// Mirrors the pattern used by SSEConsumer and GRPCConsumer.
func (s *StdoutWriter) SetMetrics(m *observability.KaptantoMetrics) {
	s.m = m
}

// ID returns the stable consumer identifier "stdout".
func (s *StdoutWriter) ID() string {
	return "stdout"
}

// Deliver writes the event as a single NDJSON line to the underlying writer.
//
// When entry.Raw is set (populated by ReadPartition), it is written directly to
// the wire followed by a newline — skipping the json.Marshal round-trip entirely.
// This is always safe for the stdout consumer because it has no column filter.
//
// mu serializes the payload+newline write sequence below against concurrent
// Deliver calls from other partition goroutines (see the package doc
// comment) — without it, two events' bytes can interleave on the wire and
// corrupt NDJSON's one-value-per-line framing.
//
// A broken pipe or closed pipe error is returned as-is; the RetryScheduler
// treats it as a permanent error (isPermanentError check) and dead-letters
// the entry immediately without waiting for maxRetries.
//
// On success, increments kaptanto_events_delivered_total if metrics were wired
// via SetMetrics. If metrics is nil (e.g. in unit tests) the counter is skipped.
func (s *StdoutWriter) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(entry.Raw) > 0 {
		// Fast path: use stored raw bytes directly, append newline for NDJSON format.
		if _, err := s.w.Write(entry.Raw); err != nil {
			return err
		}
		if _, err := s.w.Write([]byte{'\n'}); err != nil {
			return err
		}
	} else {
		// Fallback: marshal the event (e.g. when Raw is not populated in tests).
		data, err := json.Marshal(entry.Event)
		if err != nil {
			return err
		}
		if _, err := s.w.Write(data); err != nil {
			return err
		}
		if _, err := s.w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	if s.m != nil {
		s.m.EventsDelivered.WithLabelValues(s.ID(), entry.Event.Table, string(entry.Event.Operation)).Inc()
	}
	return nil
}
