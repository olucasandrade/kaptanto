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

	"github.com/kaptanto/kaptanto/internal/eventlog"
)

// StdoutWriter implements router.Consumer and writes events as NDJSON.
type StdoutWriter struct {
	enc *json.Encoder
}

// NewStdoutWriter creates a StdoutWriter that encodes events to w.
// Pass os.Stdout for production use; pass a bytes.Buffer for testing.
func NewStdoutWriter(w io.Writer) *StdoutWriter {
	return &StdoutWriter{enc: json.NewEncoder(w)}
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
func (s *StdoutWriter) Deliver(_ context.Context, entry eventlog.LogEntry) error {
	return s.enc.Encode(entry.Event)
}
