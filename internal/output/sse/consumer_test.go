package sse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/router"
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
	cs := router.NewNoopCursorStore()

	// Construct with nil rowFilter and nil allowedColumns.
	consumer := NewSSEConsumer("nil-filters", rr, filter, cs, nil, nil, nil)

	ev := makeInsertEvent(map[string]any{"id": 1, "name": "alice"})
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	body := rr.Body.String()
	assert.Contains(t, body, "id: "+ev.ID.String(), "SSE id line must be written")
	assert.Contains(t, body, "data: ", "SSE data line must be written")
}

// TestSSEConsumer_RowFilterMatchFalse_AdvancesCursorSilently verifies that when
// RowFilter.Match returns false the cursor is saved and nothing is written to the wire.
func TestSSEConsumer_RowFilterMatchFalse_AdvancesCursorSilently(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// WHERE amount > 9999 — event has amount=5 so Match returns false.
	rf, err := output.ParseRowFilter("amount > 9999")
	require.NoError(t, err)

	consumer := NewSSEConsumer("row-filter-miss", rr, filter, cs, nil, rf, nil)

	ev := makeInsertEvent(map[string]any{"id": 1, "amount": 5})
	entry := eventlog.LogEntry{Seq: 7, Event: ev}

	err = consumer.Deliver(context.Background(), entry)
	require.NoError(t, err, "filtered event must return nil error")

	// Nothing should be written to the SSE wire.
	assert.Empty(t, rr.Body.String(), "no bytes should be written to wire when RowFilter.Match is false")

	// Cursor should be advanced to seq=7 (entry.Seq, not entry.Seq+1, because
	// the event was filtered — cursor marks event as processed, not delivered+1).
	seq, cerr := cs.LoadCursor(context.Background(), "sse:row-filter-miss", 0)
	require.NoError(t, cerr)
	assert.Equal(t, uint64(7), seq, "cursor must be saved at entry.Seq when row is filtered")
}

// TestSSEConsumer_RowFilterMatchTrue_WritesToWire verifies that when
// RowFilter.Match returns true the event is written to the SSE wire.
func TestSSEConsumer_RowFilterMatchTrue_WritesToWire(t *testing.T) {
	rr := httptest.NewRecorder()
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// WHERE amount > 1 — event has amount=100 so Match returns true.
	rf, err := output.ParseRowFilter("amount > 1")
	require.NoError(t, err)

	consumer := NewSSEConsumer("row-filter-hit", rr, filter, cs, nil, rf, nil)

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
	cs := router.NewNoopCursorStore()

	// Only allow "id" — "secret" must be stripped.
	allowedColumns := []string{"id"}
	consumer := NewSSEConsumer("col-filter", rr, filter, cs, nil, nil, allowedColumns)

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

// TestSSEConsumer_Deliver_PartitionID verifies that SSEConsumer.Deliver passes
// entry.PartitionID (not a hardcoded 0) to SaveCursor on all three code paths:
// success delivery, EventFilter rejection, and RowFilter rejection.
func TestSSEConsumer_Deliver_PartitionID(t *testing.T) {
	const wantPartition = uint32(3)

	t.Run("success path saves correct partitionID", func(t *testing.T) {
		rr := httptest.NewRecorder()
		filter := output.NewEventFilter(nil, nil)
		cs := router.NewNoopCursorStore()
		consumer := NewSSEConsumer("chk02-success", rr, filter, cs, nil, nil, nil)

		ev := makeInsertEvent(map[string]any{"id": 1})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err := consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)

		// Cursor is saved as seq+1 on success path.
		seq, cerr := cs.LoadCursor(context.Background(), "sse:chk02-success", wantPartition)
		require.NoError(t, cerr)
		assert.Equal(t, uint64(11), seq, "SaveCursor must be called with partitionID=3 (not 0)")

		// Confirm nothing saved under partitionID=0.
		seqZero, _ := cs.LoadCursor(context.Background(), "sse:chk02-success", 0)
		assert.Equal(t, uint64(1), seqZero, "nothing must be saved under partitionID=0")
	})

	t.Run("EventFilter rejection saves correct partitionID", func(t *testing.T) {
		rr := httptest.NewRecorder()
		// Filter that rejects all events (table allowlist with no match).
		filter := output.NewEventFilter([]string{"other_table"}, nil)
		cs := router.NewNoopCursorStore()
		consumer := NewSSEConsumer("chk02-filter", rr, filter, cs, nil, nil, nil)

		ev := makeInsertEvent(map[string]any{"id": 2})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err := consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)

		seq, cerr := cs.LoadCursor(context.Background(), "sse:chk02-filter", wantPartition)
		require.NoError(t, cerr)
		assert.Equal(t, uint64(10), seq, "filter-rejected: SaveCursor must use partitionID=3")

		seqZero, _ := cs.LoadCursor(context.Background(), "sse:chk02-filter", 0)
		assert.Equal(t, uint64(1), seqZero, "nothing must be saved under partitionID=0")
	})

	t.Run("RowFilter rejection saves correct partitionID", func(t *testing.T) {
		rr := httptest.NewRecorder()
		filter := output.NewEventFilter(nil, nil)
		cs := router.NewNoopCursorStore()
		rf, err := output.ParseRowFilter("amount > 9999")
		require.NoError(t, err)
		consumer := NewSSEConsumer("chk02-rowfilter", rr, filter, cs, nil, rf, nil)

		ev := makeInsertEvent(map[string]any{"id": 3, "amount": 1})
		entry := eventlog.LogEntry{Seq: 10, PartitionID: wantPartition, Event: ev}

		err = consumer.Deliver(context.Background(), entry)
		require.NoError(t, err)

		seq, cerr := cs.LoadCursor(context.Background(), "sse:chk02-rowfilter", wantPartition)
		require.NoError(t, cerr)
		assert.Equal(t, uint64(10), seq, "row-filter-rejected: SaveCursor must use partitionID=3")

		seqZero, _ := cs.LoadCursor(context.Background(), "sse:chk02-rowfilter", 0)
		assert.Equal(t, uint64(1), seqZero, "nothing must be saved under partitionID=0")
	})
}

// TestSSEConsumer_ColumnFilter_DoesNotMutateSharedEvent verifies that
// ApplyColumnFilter produces a filtered copy and never mutates entry.Event
// (which is shared across consumers in the Router).
func TestSSEConsumer_ColumnFilter_DoesNotMutateSharedEvent(t *testing.T) {
	cs := router.NewNoopCursorStore()
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
	consumer := NewSSEConsumer("no-mutate", rr, filter, cs, nil, nil, []string{"id"})

	err := consumer.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// The original event's After must be unchanged.
	assert.Equal(t, string(originalAfter), string(ev.After),
		"entry.Event.After must not be mutated by column filter")
}
