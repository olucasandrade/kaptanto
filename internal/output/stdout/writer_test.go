package stdout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	dto "github.com/prometheus/client_model/go"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/observability"
	"github.com/kaptanto/kaptanto/internal/output/stdout"
	"github.com/kaptanto/kaptanto/internal/router"
)

// TestStdoutWriterID verifies that StdoutWriter.ID() returns "stdout".
func TestStdoutWriterID(t *testing.T) {
	w := stdout.NewStdoutWriter(new(bytes.Buffer))
	if got := w.ID(); got != "stdout" {
		t.Errorf("ID() = %q, want %q", got, "stdout")
	}
}

// TestStdoutWriterImplementsConsumer is a compile-time assertion.
func TestStdoutWriterImplementsConsumer(t *testing.T) {
	var _ router.Consumer = stdout.NewStdoutWriter(new(bytes.Buffer))
}

// TestStdoutWriterNDJSON verifies that Deliver writes exactly one JSON line per
// event (NDJSON format): valid JSON terminated by a newline.
func TestStdoutWriterNDJSON(t *testing.T) {
	var buf bytes.Buffer
	w := stdout.NewStdoutWriter(&buf)

	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	ev := &event.ChangeEvent{
		ID:        id,
		Operation: event.OpInsert,
		Table:     "orders",
		Key:       json.RawMessage(`{"id":42}`),
	}
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	if err := w.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("Deliver error: %v", err)
	}

	out := buf.String()

	// Must end with exactly one newline (NDJSON).
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output does not end with newline: %q", out)
	}

	// Must be valid JSON.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out)
	}

	// ID field must round-trip correctly.
	gotID, ok := decoded["id"].(string)
	if !ok {
		t.Fatalf("decoded id is not a string: %T", decoded["id"])
	}
	if gotID != id.String() {
		t.Errorf("id mismatch: got %q, want %q", gotID, id.String())
	}
}

// TestStdoutWriterNilMetricsNoPanic verifies that Deliver without SetMetrics
// does not panic and encodes correctly.
func TestStdoutWriterNilMetricsNoPanic(t *testing.T) {
	var buf bytes.Buffer
	w := stdout.NewStdoutWriter(&buf) // no SetMetrics call

	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	ev := &event.ChangeEvent{
		ID:        id,
		Operation: event.OpInsert,
		Table:     "users",
		Key:       json.RawMessage(`{"id":1}`),
	}
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	// Must not panic.
	if err := w.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("Deliver with nil metrics returned error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected output but buffer is empty")
	}
}

// TestStdoutWriterMetricsIncrement verifies that Deliver with a real
// KaptantoMetrics instance increments EventsDelivered on success.
func TestStdoutWriterMetricsIncrement(t *testing.T) {
	var buf bytes.Buffer
	w := stdout.NewStdoutWriter(&buf)

	m := observability.NewKaptantoMetrics()
	w.SetMetrics(m)

	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	ev := &event.ChangeEvent{
		ID:        id,
		Operation: event.OpInsert,
		Table:     "orders",
		Key:       json.RawMessage(`{"id":99}`),
	}
	entry := eventlog.LogEntry{Seq: 2, Event: ev}

	if err := w.Deliver(context.Background(), entry); err != nil {
		t.Fatalf("Deliver error: %v", err)
	}

	// Gather metrics and verify counter is 1.
	counter, err := m.EventsDelivered.GetMetricWithLabelValues("stdout", "orders", "insert")
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	var d dto.Metric
	if err := counter.Write(&d); err != nil {
		t.Fatalf("metric Write: %v", err)
	}
	if got := d.Counter.GetValue(); got != 1.0 {
		t.Errorf("EventsDelivered = %v, want 1.0", got)
	}
}

// TestStdoutWriterEncodeErrorNoIncrement verifies that an encode error (write
// to a closed PipeWriter) does NOT increment EventsDelivered.
func TestStdoutWriterEncodeErrorNoIncrement(t *testing.T) {
	pr, pw := io.Pipe()
	_ = pr.Close() // close the read end so writes will fail
	pw.Close()     // close the write end too

	w := stdout.NewStdoutWriter(pw)
	m := observability.NewKaptantoMetrics()
	w.SetMetrics(m)

	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	ev := &event.ChangeEvent{
		ID:        id,
		Operation: event.OpUpdate,
		Table:     "orders",
		Key:       json.RawMessage(`{"id":5}`),
	}
	entry := eventlog.LogEntry{Seq: 3, Event: ev}

	err := w.Deliver(context.Background(), entry)
	if err == nil {
		t.Fatal("expected encode error but got nil")
	}

	// Counter must remain at zero.
	counter, err2 := m.EventsDelivered.GetMetricWithLabelValues("stdout", "orders", "update")
	if err2 != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err2)
	}
	var d dto.Metric
	if err2 = counter.Write(&d); err2 != nil {
		t.Fatalf("metric Write: %v", err2)
	}
	if got := d.Counter.GetValue(); got != 0.0 {
		t.Errorf("EventsDelivered after error = %v, want 0.0", got)
	}
}
