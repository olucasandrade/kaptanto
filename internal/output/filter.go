// Package output provides shared utilities for kaptanto output consumers
// (SSE, gRPC, stdout). It includes event filtering (CFG-03, CFG-04) used by
// all consumer types to restrict which events are delivered.
package output

import "github.com/kaptanto/kaptanto/internal/event"

// EventFilter restricts which events a consumer receives based on configured
// table and operation allow-lists.
//
// A nil Tables map means all tables are allowed (CFG-03).
// A nil Operations map means all operations are allowed (CFG-04).
//
// Returning nil from Deliver for filtered events is NOT an error; the cursor
// advances normally. The filter is applied before dispatching to Deliver.
type EventFilter struct {
	// Tables is the set of table names that are allowed through the filter.
	// If nil, all tables are allowed.
	Tables map[string]struct{}

	// Operations is the set of operations that are allowed through the filter.
	// If nil, all operations are allowed.
	Operations map[event.Operation]struct{}
}

// NewEventFilter constructs an EventFilter from string slices.
// Pass nil or an empty slice for tables to allow all tables (CFG-03).
// Pass nil or an empty slice for operations to allow all operations (CFG-04).
func NewEventFilter(tables []string, operations []string) *EventFilter {
	f := &EventFilter{}
	if len(tables) > 0 {
		f.Tables = make(map[string]struct{}, len(tables))
		for _, t := range tables {
			f.Tables[t] = struct{}{}
		}
	}
	if len(operations) > 0 {
		f.Operations = make(map[event.Operation]struct{}, len(operations))
		for _, op := range operations {
			f.Operations[event.Operation(op)] = struct{}{}
		}
	}
	return f
}

// Allow returns true if the event passes all configured filter criteria.
// Table and operation checks are independent and both must pass.
func (f *EventFilter) Allow(ev *event.ChangeEvent) bool {
	if f.Tables != nil {
		if _, ok := f.Tables[ev.Table]; !ok {
			return false
		}
	}
	if f.Operations != nil {
		if _, ok := f.Operations[ev.Operation]; !ok {
			return false
		}
	}
	return true
}
