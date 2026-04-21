package output

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// makeEvent is a helper that constructs a minimal ChangeEvent for filter tests.
func makeEvent(table string, op event.Operation) *event.ChangeEvent {
	return &event.ChangeEvent{
		Table:     table,
		Operation: op,
	}
}

// TestEventFilterNilTablesPassThrough verifies that Allow returns true when
// Tables is nil (all tables pass through — CFG-03).
func TestEventFilterNilTablesPassThrough(t *testing.T) {
	f := NewEventFilter(nil, []string{"insert"})
	ev := makeEvent("orders", event.OpInsert)
	if !f.Allow(ev) {
		t.Error("Allow = false with nil Tables, want true")
	}
}

// TestEventFilterTableExclusion verifies that Allow returns false when
// event.Table is not in the configured Tables set.
func TestEventFilterTableExclusion(t *testing.T) {
	f := NewEventFilter([]string{"orders"}, nil)
	ev := makeEvent("payments", event.OpInsert)
	if f.Allow(ev) {
		t.Error("Allow = true for excluded table, want false")
	}
}

// TestEventFilterTableInclusion verifies that Allow returns true when
// event.Table is in the configured Tables set.
func TestEventFilterTableInclusion(t *testing.T) {
	f := NewEventFilter([]string{"orders", "payments"}, nil)
	ev := makeEvent("orders", event.OpInsert)
	if !f.Allow(ev) {
		t.Error("Allow = false for included table, want true")
	}
}

// TestEventFilterOperationExclusion verifies that Allow returns false when
// event.Operation is not in the configured Operations set.
func TestEventFilterOperationExclusion(t *testing.T) {
	f := NewEventFilter(nil, []string{"insert", "update"})
	ev := makeEvent("orders", event.OpDelete)
	if f.Allow(ev) {
		t.Error("Allow = true for excluded operation, want false")
	}
}

// TestEventFilterNilOperationsPassThrough verifies that Allow returns true when
// Operations is nil (all operations pass through — CFG-04).
func TestEventFilterNilOperationsPassThrough(t *testing.T) {
	f := NewEventFilter([]string{"orders"}, nil)
	ev := makeEvent("orders", event.OpDelete)
	if !f.Allow(ev) {
		t.Error("Allow = false with nil Operations, want true")
	}
}

// TestEventFilterBothCriteriaCombined verifies that Allow returns true only
// when both Table and Operation match their respective allow-lists.
func TestEventFilterBothCriteriaCombined(t *testing.T) {
	f := NewEventFilter([]string{"orders"}, []string{"insert", "update"})

	cases := []struct {
		table string
		op    event.Operation
		want  bool
		desc  string
	}{
		{"orders", event.OpInsert, true, "table+op both match"},
		{"orders", event.OpDelete, false, "table matches, op excluded"},
		{"payments", event.OpInsert, false, "op matches, table excluded"},
		{"payments", event.OpDelete, false, "neither matches"},
	}

	for _, tc := range cases {
		ev := makeEvent(tc.table, tc.op)
		got := f.Allow(ev)
		if got != tc.want {
			t.Errorf("[%s] Allow(%q, %q) = %v, want %v", tc.desc, tc.table, tc.op, got, tc.want)
		}
	}
}
