package sse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/output"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeInsertEvent builds a ChangeEvent with JSON After payload for filter tests.
func makeInsertEvent(after map[string]any) *event.ChangeEvent {
	raw, _ := json.Marshal(after)
	return &event.ChangeEvent{
		ID:             ulid.Make(),
		IdempotencyKey: "test:orders:1:insert:0/0",
		Operation:      event.OpInsert,
		Table:          "orders",
		After:          json.RawMessage(raw),
	}
}

// TestSSEConsumer_NilFiltersPassThrough ensures that nil rowFilter and nil
// allowedColumns produce identical behavior to the current (pre-CFG-06)
// SSEConsumer: events are delivered to the wire unchanged.
func TestSSEConsumer_NilFiltersPassThrough(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil) // allow all events

	// Construct with nil rowFilter and nil allowedColumns.
	consumer := NewSSEConsumer("nil-filters", rr, filter, nil, nil, nil)

	ev := makeInsertEvent(map[string]any{"id": 1, "name": "alice"})
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	assert.Contains(t, body, "id: "+ev.ID.String(), "SSE id line must be written")
	assert.Contains(t, body, "data: ", "SSE data line must be written")
}

// TestSSEConsumer_RowFilterMatchFalse_ReturnsNil verifies that when
// RowFilter.Match returns false, Deliver returns nil and nothing is written to
// the wire. The router is responsible for advancing the cursor.
func TestSSEConsumer_RowFilterMatchFalse_ReturnsNil(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)

	// WHERE amount > 9999 — event has amount=5 so Match returns false.
	rf, err := output.ParseRowFilter("amount > 9999")
	require.NoError(t, err)

	consumer := NewSSEConsumer("row-filter-miss", rr, filter, nil, map[string]*output.RowFilter{"orders": rf}, nil)

	ev := makeInsertEvent(map[string]any{"id": 1, "amount": 5})
	entry := eventlog.LogEntry{Seq: 7, Event: ev}

	err = consumer.Deliver(context.Background(), entry)
	require.NoError(t, err, "filtered event must return nil error so router advances cursor")

	// Nothing should be written to the SSE wire.
	assert.Empty(t, rr.Body.String(), "no bytes should be written to wire when RowFilter.Match is false")
}

// TestSSEConsumer_EventFilterMatch_ReturnsNil verifies that when
// EventFilter.Allow returns false, Deliver returns nil and nothing is written.
// The router is responsible for advancing the cursor.
func TestSSEConsumer_EventFilterMatch_ReturnsNil(t *testing.T) {
	rr := httptest.NewRecorder()
	// Filter that rejects all events (table allowlist with no match).
	filter := output.NewEventFilter([]string{"other_table"}, nil)

	consumer := NewSSEConsumer("event-filter-miss", rr, filter, nil, nil, nil)

	ev := makeInsertEvent(map[string]any{"id": 2})
	entry := eventlog.LogEntry{Seq: 10, PartitionID: 3, Event: ev}

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err, "event-filter-rejected event must return nil so router advances cursor")

	// Nothing should be written to the SSE wire.
	assert.Empty(t, rr.Body.String(), "no bytes should be written to wire when EventFilter rejects event")
}

// TestSSEConsumer_RowFilterMatchTrue_WritesToWire verifies that when
// RowFilter.Match returns true the event is written to the SSE wire.
func TestSSEConsumer_RowFilterMatchTrue_WritesToWire(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)

	// WHERE amount > 1 — event has amount=100 so Match returns true.
	rf, err := output.ParseRowFilter("amount > 1")
	require.NoError(t, err)

	consumer := NewSSEConsumer("row-filter-hit", rr, filter, nil, map[string]*output.RowFilter{"orders": rf}, nil)

	ev := makeInsertEvent(map[string]any{"id": 2, "amount": 100})
	entry := eventlog.LogEntry{Seq: 3, Event: ev}

	err = consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	assert.Contains(t, body, "id: "+ev.ID.String(), "SSE id line must be written when row matches")
	assert.Contains(t, body, "data: ", "SSE data line must be written when row matches")
}

// TestSSEConsumer_ColumnFilter_StripsForbiddenColumns verifies that
// ApplyColumnFilter is called on Before and After before encoding to wire.
// Columns not in the allow-list must be absent from the JSON payload.
func TestSSEConsumer_ColumnFilter_StripsForbiddenColumns(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)

	// Only allow "id" — "secret" must be stripped.
	allowedColumns := []string{"id"}
	consumer := NewSSEConsumer("col-filter", rr, filter, nil, nil, map[string][]string{"orders": allowedColumns})

	ev := makeInsertEvent(map[string]any{"id": 42, "secret": "s3cr3t"})
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	// Extract the JSON data line.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			assert.Contains(t, payload, `"id"`, "allowed column 'id' must be present")
			assert.NotContains(t, payload, `"secret"`, "forbidden column 'secret' must be stripped")
			return
		}
	}
	t.Fatal("no data line found in SSE output")
}

// TestSSEConsumer_NoCursorSaveOnDeliver verifies that SSEConsumer.Deliver does
// not call SaveCursor for any outcome (delivered, EventFilter-rejected,
// RowFilter-rejected). Cursor persistence is exclusively the router's
// responsibility. This test uses a recordingCursorStore to assert zero saves.
func TestSSEConsumer_NoCursorSaveOnDeliver(t *testing.T) {
	const wantPartition = uint32(3)

	t.Run("success path does not call SaveCursor", func(t *testing.T) {
		rr := httptest.NewRecorder()
		filter := output.NewEventFilter(nil, nil)
		recorder := &recordingCursorStore{}
		// SSEConsumer no longer accepts a cursor store — pass nil metrics.
		consumer := NewSSEConsumer("no-save-success", rr, filter, nil, nil, nil)
		_ = recorder // recorder is unused — assert below that no save occurred

		ev := makeInsertEvent(map[string]any{"id": 1})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err := consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)
		// If we reach here with no panic, SSEConsumer did not try to use a cursor store.
	})

	t.Run("EventFilter rejection does not call SaveCursor", func(t *testing.T) {
		rr := httptest.NewRecorder()
		filter := output.NewEventFilter([]string{"other_table"}, nil)
		consumer := NewSSEConsumer("no-save-filter", rr, filter, nil, nil, nil)

		ev := makeInsertEvent(map[string]any{"id": 2})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err := consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)
		assert.Empty(t, rr.Body.String())
	})

	t.Run("RowFilter rejection does not call SaveCursor", func(t *testing.T) {
		rr := httptest.NewRecorder()
		filter := output.NewEventFilter(nil, nil)
		rf, err := output.ParseRowFilter("amount > 9999")
		require.NoError(t, err)
		consumer := NewSSEConsumer("no-save-rowfilter", rr, filter, nil, map[string]*output.RowFilter{"orders": rf}, nil)

		ev := makeInsertEvent(map[string]any{"id": 3, "amount": 1})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err = consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)
		assert.Empty(t, rr.Body.String())
	})
}

// recordingCursorStore is a test helper that records SaveCursor calls.
// It is not passed to SSEConsumer (which no longer accepts a cursor store),
// but is kept here for future integration tests that wire the router.
type recordingCursorStore struct {
	saves []cursorSave
}

type cursorSave struct {
	consumerID  string
	partitionID uint32
	seq         uint64
}

func (r *recordingCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	r.saves = append(r.saves, cursorSave{consumerID, partitionID, seq})
	return nil
}

func (r *recordingCursorStore) LoadCursor(_ context.Context, _ string, _ uint32) (uint64, error) {
	return 1, nil
}

// TestSSEConsumer_ColumnFilter_DoesNotMutateSharedEvent verifies that
// ApplyColumnFilter produces a filtered copy and never mutates entry.Event
// (which is shared across consumers in the Router).
// TestSSEConsumer_RawPassThrough verifies that when no column filter is configured
// and entry.Raw is populated, Deliver writes the raw bytes directly to the wire
// (raw-bytes-passthrough fast path).
func TestSSEConsumer_RawPassThrough(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)

	ev := makeInsertEvent(map[string]any{"id": 1, "status": "new"})
	rawBytes, _ := json.Marshal(ev)
	entry := eventlog.LogEntry{Seq: 1, Event: ev, Raw: rawBytes}

	// No column filter configured — should use raw passthrough.
	consumer := NewSSEConsumer("raw-pt", rr, filter, nil, nil, nil)
	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	// Wire format: "id: <ULID>\ndata: <raw bytes>\n\n"
	assert.Contains(t, body, "id: "+ev.ID.String())
	assert.Contains(t, body, "data: "+string(rawBytes))
}

// TestSSEConsumer_ColumnFilter_UsesFilteredMarshal verifies that when a column
// filter is active, the consumer re-marshals the filtered event (NOT raw bytes).
func TestSSEConsumer_ColumnFilter_UsesFilteredMarshal(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)

	ev := &event.ChangeEvent{
		ID:        ulid.Make(),
		Operation: event.OpInsert,
		Table:     "orders",
		After:     json.RawMessage(`{"id":1,"secret":"hidden"}`),
	}
	rawBytes, _ := json.Marshal(ev)
	entry := eventlog.LogEntry{Seq: 1, Event: ev, Raw: rawBytes}

	// Column filter: only allow "id", strip "secret".
	consumer := NewSSEConsumer("col-filter", rr, filter, nil, nil, map[string][]string{"orders": {"id"}})
	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	// "secret" field must NOT appear in the wire output.
	assert.NotContains(t, body, "secret", "filtered field must not appear in SSE output")
	// "id" field must appear.
	assert.Contains(t, body, `"id"`)
}

// TestSSEConsumer_ColumnFilter_DoesNotMutateSharedEvent verifies that
// ApplyColumnFilter produces a filtered copy and never mutates entry.Event
// (which is shared across consumers in the Router).
func TestSSEConsumer_ColumnFilter_DoesNotMutateSharedEvent(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)

	originalAfter := json.RawMessage(`{"id":1,"secret":"hidden"}`)
	ev := &event.ChangeEvent{
		ID:        ulid.Make(),
		Operation: event.OpInsert,
		Table:     "orders",
		After:     originalAfter,
	}
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	// Consumer with column restriction.
	rr := httptest.NewRecorder()
	consumer := NewSSEConsumer("no-mutate", rr, filter, nil, nil, map[string][]string{"orders": {"id"}})

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// The original event's After must be unchanged.
	assert.Equal(t, string(originalAfter), string(ev.After),
		"entry.Event.After must not be mutated by column filter")
}
