package grpcoutput

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/output"
	"github.com/kaptanto/kaptanto/internal/router"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeGRPCInsertEvent builds a ChangeEvent with JSON After payload for filter tests.
func makeGRPCInsertEvent(after map[string]any) *event.ChangeEvent {
	raw, _ := json.Marshal(after)
	return &event.ChangeEvent{
		ID:             ulid.Make(),
		IdempotencyKey: "test:orders:1:insert:0/0",
		Operation:      event.OpInsert,
		Table:          "orders",
		After:          json.RawMessage(raw),
	}
}

// TestGRPCConsumer_NilFiltersPassThrough ensures that nil rowFilter and nil
// allowedColumns produce identical behavior to the current consumer: events
// are encoded and sent to the channel unchanged.
func TestGRPCConsumer_NilFiltersPassThrough(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// Construct with nil rowFilter and nil allowedColumns.
	c := NewGRPCConsumer("nil-filters", 8, filter, cs, nil, nil, nil)

	ev := makeGRPCInsertEvent(map[string]any{"id": 1, "name": "alice"})
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// Event should be in the channel.
	select {
	case protoEv := <-c.ch:
		assert.Equal(t, ev.ID.String(), protoEv.Id)
		assert.NotEmpty(t, protoEv.Payload, "payload must be non-empty")
	default:
		t.Fatal("expected event in channel but channel was empty")
	}
}

// TestGRPCConsumer_RowFilterMatchFalse_ReturnsNil verifies that when
// RowFilter.Match returns false, Deliver returns nil (cursor advances via Router)
// and nothing is sent to the channel.
func TestGRPCConsumer_RowFilterMatchFalse_ReturnsNil(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// WHERE amount > 9999 — event has amount=5 so Match returns false.
	rf, err := output.ParseRowFilter("amount > 9999")
	require.NoError(t, err)

	c := NewGRPCConsumer("row-filter-miss", 8, filter, cs, nil, rf, nil)

	ev := makeGRPCInsertEvent(map[string]any{"id": 1, "amount": 5})
	entry := eventlog.LogEntry{Seq: 7, Event: ev}

	err = c.Deliver(context.Background(), entry)
	require.NoError(t, err, "filtered event must return nil (Router advances cursor)")

	// Nothing should be in the channel.
	select {
	case <-c.ch:
		t.Fatal("channel should be empty when RowFilter.Match is false")
	default:
		// expected: channel empty
	}
}

// TestGRPCConsumer_RowFilterMatchTrue_SendsToChannel verifies that when
// RowFilter.Match returns true, the event is encoded and sent to the channel.
func TestGRPCConsumer_RowFilterMatchTrue_SendsToChannel(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// WHERE amount > 1 — event has amount=100 so Match returns true.
	rf, err := output.ParseRowFilter("amount > 1")
	require.NoError(t, err)

	c := NewGRPCConsumer("row-filter-hit", 8, filter, cs, nil, rf, nil)

	ev := makeGRPCInsertEvent(map[string]any{"id": 2, "amount": 100})
	entry := eventlog.LogEntry{Seq: 3, Event: ev}

	err = c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	select {
	case protoEv := <-c.ch:
		assert.Equal(t, ev.ID.String(), protoEv.Id)
		assert.NotEmpty(t, protoEv.Payload)
	default:
		t.Fatal("expected event in channel when RowFilter.Match is true")
	}
}

// TestGRPCConsumer_ColumnFilter_StripsForbiddenColumns verifies that
// ApplyColumnFilter is applied before json.Marshal so forbidden columns
// are absent from the Payload field.
func TestGRPCConsumer_ColumnFilter_StripsForbiddenColumns(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	// Only allow "id" — "secret" must be stripped from Payload.
	allowedColumns := []string{"id"}
	c := NewGRPCConsumer("col-filter", 8, filter, cs, nil, nil, allowedColumns)

	ev := makeGRPCInsertEvent(map[string]any{"id": 42, "secret": "s3cr3t"})
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	select {
	case protoEv := <-c.ch:
		assert.NotEmpty(t, protoEv.Payload)
		// Payload is the full JSON-encoded event; After field should only have "id".
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(protoEv.Payload, &decoded))
		after, ok := decoded["after"].(map[string]any)
		require.True(t, ok, "after field must be a JSON object")
		assert.Contains(t, after, "id", "allowed column 'id' must be present")
		assert.NotContains(t, after, "secret", "forbidden column 'secret' must be stripped")
	default:
		t.Fatal("expected event in channel")
	}
}

// TestGRPCConsumer_ColumnFilter_DoesNotMutateSharedEvent verifies that
// ApplyColumnFilter produces a filtered copy and never mutates entry.Event.
func TestGRPCConsumer_ColumnFilter_DoesNotMutateSharedEvent(t *testing.T) {
	filter := output.NewEventFilter(nil, nil)
	cs := router.NewNoopCursorStore()

	originalAfter := json.RawMessage(`{"id":1,"secret":"hidden"}`)
	ev := &event.ChangeEvent{
		ID:        ulid.Make(),
		Operation: event.OpInsert,
		Table:     "orders",
		After:     originalAfter,
	}
	entry := eventlog.LogEntry{Seq: 1, Event: ev}

	c := NewGRPCConsumer("no-mutate", 8, filter, cs, nil, nil, []string{"id"})

	err := c.Deliver(context.Background(), entry)
	require.NoError(t, err)

	// Drain channel.
	select {
	case <-c.ch:
	default:
		t.Fatal("expected event in channel")
	}

	// The original event's After must be unchanged.
	assert.Equal(t, string(originalAfter), string(ev.After),
		"entry.Event.After must not be mutated by column filter")
}
