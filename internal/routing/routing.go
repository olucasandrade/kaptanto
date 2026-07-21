// Package routing provides compiled match-rule evaluation for the kaptanto
// action/routing layer. Each MatchConfig compiles into a Matcher at startup
// (RTG-01); Match calls on the hot path are allocation-free on miss (RTG-02).
package routing

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/output"
)

// MatchConfig declares a single match rule in YAML/JSON configuration.
type MatchConfig struct {
	Tables     []string `yaml:"tables"`     // globs: "public.orders", "public.*", "*"; empty = all
	Operations []string `yaml:"operations"` // insert|update|delete|read; empty = all
	Where      string   `yaml:"where"`      // existing WHERE grammar; empty = always true
}

// opBitmask encodes a set of operations as bitflags for O(1) membership check.
type opBitmask uint8

const (
	opInsert opBitmask = 1 << iota
	opUpdate
	opDelete
	opRead
	opAll = opInsert | opUpdate | opDelete | opRead
)

var canonicalOps = map[string]opBitmask{
	"insert": opInsert,
	"update": opUpdate,
	"delete": opDelete,
	"read":   opRead,
}

// Matcher is the compiled form of a MatchConfig. Its Match method is safe for
// concurrent use and performs no allocations on the miss path.
type Matcher struct {
	tables    []tableGlob
	matchAll  bool // true when Tables is empty (match all tables)
	ops       opBitmask
	rowFilter *output.RowFilter
}

// Compile validates and compiles mc into a Matcher. It returns a descriptive
// error naming the offending field and value on any validation failure.
// Compile must be called at startup; any error should abort the process (RTG-01).
func Compile(mc MatchConfig) (*Matcher, error) {
	m := &Matcher{}

	// --- Tables ---
	if len(mc.Tables) == 0 {
		m.matchAll = true
	} else {
		m.tables = make([]tableGlob, len(mc.Tables))
		for i, pat := range mc.Tables {
			g, err := compileTableGlob(pat)
			if err != nil {
				return nil, fmt.Errorf("compile match rule: %w", err)
			}
			m.tables[i] = g
		}
	}

	// --- Operations ---
	if len(mc.Operations) == 0 {
		m.ops = opAll
	} else {
		for _, raw := range mc.Operations {
			op := strings.ToLower(strings.TrimSpace(raw))
			bit, ok := canonicalOps[op]
			if !ok {
				return nil, fmt.Errorf("compile match rule: operations: unknown operation %q", raw)
			}
			m.ops |= bit
		}
	}

	// --- WHERE ---
	rf, err := output.ParseRowFilter(mc.Where)
	if err != nil {
		return nil, fmt.Errorf("compile match rule: where: %w", err)
	}
	m.rowFilter = rf

	return m, nil
}

// Match reports whether ev satisfies this matcher's tables AND operations AND
// where clause. It performs no heap allocation on the miss path and no I/O ever.
// A non-nil error means the row data is malformed and the caller must treat it
// as a permanent delivery failure.
func (m *Matcher) Match(ev *event.ChangeEvent) (bool, error) {
	// Fast-path: operation bitmask check.
	if !m.matchOp(ev.Operation) {
		return false, nil
	}

	// Table glob check.
	if !m.matchAll {
		name := qualifiedName(ev.Schema, ev.Table)
		if !m.matchTable(name) {
			return false, nil
		}
	}

	// WHERE row filter (always true for nil/empty filter).
	return m.rowFilter.Match(ev)
}

// matchOp checks the operation against the compiled bitmask.
func (m *Matcher) matchOp(op event.Operation) bool {
	switch op {
	case event.OpInsert:
		return m.ops&opInsert != 0
	case event.OpUpdate:
		return m.ops&opUpdate != 0
	case event.OpDelete:
		return m.ops&opDelete != 0
	case event.OpRead:
		return m.ops&opRead != 0
	default:
		return false
	}
}

// matchTable checks the qualified table name against all compiled globs.
func (m *Matcher) matchTable(name string) bool {
	for i := range m.tables {
		if m.tables[i].match(name) {
			return true
		}
	}
	return false
}

// qualifiedName builds "schema.table" or just "table" when schema is empty.
func qualifiedName(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}
