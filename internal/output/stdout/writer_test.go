package stdout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
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
