// Package routing provides event matching for action routing (RTG-01).
//
// A MatchConfig specifies which events should be delivered to an action based
// on table names and operations. Compile validates the config at startup (any
// error aborts startup per RTG-01) and returns a Matcher that evaluates each
// event in O(1) via pre-built lookup maps.
package routing

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// MatchConfig declares which events an action should receive.
// An empty Tables slice means "match all tables"; an empty Operations slice
// means "match all operations". Both conditions must pass (AND).
type MatchConfig struct {
	Tables     []string `yaml:"tables"`     // e.g. ["public.orders", "public.users"]
	Operations []string `yaml:"operations"` // e.g. ["insert", "update"]
}

// Matcher evaluates a ChangeEvent against a compiled MatchConfig.
// Zero-alloc on the hot path (lookup maps built once at Compile time).
type Matcher struct {
	tables     map[string]struct{} // nil = match all
	operations map[event.Operation]struct{} // nil = match all
}

// validOperations is the set of known operations for validation.
var validOperations = map[string]struct{}{
	"insert":  {},
	"update":  {},
	"delete":  {},
	"read":    {},
	"control": {},
}

// Compile validates cfg and returns a Matcher. Returns an error if any
// operation name is invalid. An empty/nil cfg matches all events.
func Compile(cfg MatchConfig) (*Matcher, error) {
	m := &Matcher{}

	if len(cfg.Tables) > 0 {
		m.tables = make(map[string]struct{}, len(cfg.Tables))
		for _, t := range cfg.Tables {
			t = strings.TrimSpace(t)
			if t == "" {
				return nil, fmt.Errorf("routing: empty table name in match config")
			}
			m.tables[t] = struct{}{}
		}
	}

	if len(cfg.Operations) > 0 {
		m.operations = make(map[event.Operation]struct{}, len(cfg.Operations))
		for _, op := range cfg.Operations {
			op = strings.TrimSpace(strings.ToLower(op))
			if _, ok := validOperations[op]; !ok {
				return nil, fmt.Errorf("routing: unknown operation %q (valid: insert, update, delete, read, control)", op)
			}
			m.operations[event.Operation(op)] = struct{}{}
		}
	}

	return m, nil
}

// Match returns true when the event passes this matcher's table and operation
// filters. A nil event never matches.
func (m *Matcher) Match(e *event.ChangeEvent) bool {
	if e == nil {
		return false
	}
	if m.tables != nil {
		key := e.Table
		if e.Schema != "" {
			key = e.Schema + "." + e.Table
		}
		if _, ok := m.tables[key]; !ok {
			return false
		}
	}
	if m.operations != nil {
		if _, ok := m.operations[e.Operation]; !ok {
			return false
		}
	}
	return true
}

// MatchAll returns a Matcher that matches every event.
func MatchAll() *Matcher {
	return &Matcher{}
}
