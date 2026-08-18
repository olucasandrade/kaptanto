package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/routing"
)

// redactedValue is the sentinel written in place of a masked column value.
// Keys remain so schema stays visible (MCP-01).
const redactedValue = "***"

// redactRule is one compiled redact block: table globs + column set.
type redactRule struct {
	matcher *routing.Matcher
	columns map[string]struct{}
}

// ACL enforces a single API key's table allow-list and column redaction
// (MCP-01). There is no unfiltered code path through Apply.
type ACL struct {
	name           string
	matcher        *routing.Matcher // tables ACL; empty Tables ⇒ match all
	allowAllTables bool
	rules          []redactRule
}

// CompileACL validates and compiles a key's tables + redact rules.
func CompileACL(key config.MCPAPIKey) (*ACL, error) {
	if len(key.Tables) == 0 {
		return nil, fmt.Errorf("mcp: key %q: tables is required (use [\"*\"] to allow all tables)", key.Name)
	}
	m, err := routing.Compile(routing.MatchConfig{Tables: key.Tables})
	if err != nil {
		return nil, fmt.Errorf("mcp: key %q tables: %w", key.Name, err)
	}
	rules := make([]redactRule, 0, len(key.Redact))
	for i, r := range key.Redact {
		if len(r.Columns) == 0 {
			continue
		}
		rm, err := routing.Compile(routing.MatchConfig{Tables: r.Tables})
		if err != nil {
			return nil, fmt.Errorf("mcp: key %q redact[%d] tables: %w", key.Name, i, err)
		}
		cols := make(map[string]struct{}, len(r.Columns))
		for _, c := range r.Columns {
			cols[c] = struct{}{}
		}
		rules = append(rules, redactRule{matcher: rm, columns: cols})
	}
	return &ACL{
		name:           key.Name,
		matcher:        m,
		allowAllTables: len(key.Tables) == 1 && key.Tables[0] == "*",
		rules:          rules,
	}, nil
}

// Name returns the API key name (never the secret material).
func (a *ACL) Name() string { return a.name }

// AllowTable reports whether the key may see the given qualified table name
// (e.g. "public.orders").
func (a *ACL) AllowTable(qualified string) bool {
	if a.allowAllTables {
		return true
	}
	schema, table := splitQualified(qualified)
	ok, err := a.matcher.Match(&event.ChangeEvent{
		Schema:    schema,
		Table:     table,
		Operation: event.OpRead,
	})
	return err == nil && ok
}

// FilterTables returns the subset of qualified table names this key may see.
func (a *ACL) FilterTables(tables []string) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		if a.AllowTable(t) {
			out = append(out, t)
		}
	}
	return out
}

// Apply filters an event through ACL + redaction (MCP-01).
// Returns (nil, false) when the table is not allowed; otherwise a copy with
// redacted column values. The input event is never mutated.
func (a *ACL) Apply(ev *event.ChangeEvent) (*event.ChangeEvent, bool) {
	if ev == nil {
		return nil, false
	}
	qualified := qualifiedName(ev.Schema, ev.Table)
	if !a.AllowTable(qualified) {
		return nil, false
	}
	out := *ev
	cols := a.columnsFor(qualified)
	if len(cols) == 0 {
		// No columns are redacted, so return the allowed event copy unchanged.
		// AIContext is intentionally retained here; it is only cleared when at
		// least one column is being redacted (see TestDrain_AIContextRipple).
		return &out, true
	}
	out.Before = maskColumns(out.Before, cols)
	out.After = maskColumns(out.After, cols)
	out.AIContext = nil
	return &out, true
}

// IsColumnRedacted reports whether column is masked for the given table.
func (a *ACL) IsColumnRedacted(qualified, column string) bool {
	cols := a.columnsFor(qualified)
	_, ok := cols[column]
	return ok
}

func (a *ACL) columnsFor(qualified string) map[string]struct{} {
	if len(a.rules) == 0 {
		return nil
	}
	schema, table := splitQualified(qualified)
	probe := &event.ChangeEvent{Schema: schema, Table: table, Operation: event.OpRead}
	merged := make(map[string]struct{})
	for _, r := range a.rules {
		ok, err := r.matcher.Match(probe)
		if err != nil || !ok {
			continue
		}
		for c := range r.columns {
			merged[c] = struct{}{}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func maskColumns(raw json.RawMessage, cols map[string]struct{}) json.RawMessage {
	if raw == nil || len(cols) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw // malformed: pass through
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return raw
	}
	changed := false
	for k := range cols {
		if _, exists := obj[k]; exists {
			obj[k] = redactedValue
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return json.RawMessage(out)
}

func qualifiedName(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}

func splitQualified(name string) (schema, table string) {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}
