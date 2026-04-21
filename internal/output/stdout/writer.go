// Package stdout provides a router.Consumer implementation that writes each
// delivered event as a single NDJSON line to an io.Writer (typically os.Stdout).
//
// StdoutWriter is NOT goroutine-safe. It is intended for use as a single
// registered consumer; the Router delivers events sequentially per consumer
// within a message group, which satisfies this invariant.
package stdout

import (
	"context"
	"encoding/json"
	"io"

	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// StdoutWriter implements router.Consumer and writes events as NDJSON.
type StdoutWriter struct {
	enc *json.Encoder
	m   *observability.KaptantoMetrics
}

// NewStdoutWriter creates a StdoutWriter that encodes events to w.
// Pass os.Stdout for production use; pass a bytes.Buffer for testing.
func NewStdoutWriter(w io.Writer) *StdoutWriter {
	return &StdoutWriter{enc: json.NewEncoder(w)}
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

// Deliver encodes entry.Event as a single JSON line (NDJSON) to the underlying
// writer. json.Encoder.Encode appends a trailing newline automatically.
//
// A broken pipe or closed pipe error is returned as-is; the RetryScheduler
// treats it as a permanent error (isPermanentError check) and dead-letters
// the entry immediately without waiting for maxRetries.
//
// On success, increments kaptanto_events_delivered_total if metrics were wired
// via SetMetrics. If metrics is nil (e.g. in unit tests) the counter is skipped.
func (s *StdoutWriter) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	if err := s.enc.Encode(entry.Event); err != nil {
		return err
	}
	if s.m != nil {
		s.m.EventsDelivered.WithLabelValues(s.ID(), entry.Event.Table, string(entry.Event.Operation)).Inc()
	}
	return nil
}
