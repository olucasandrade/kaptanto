// Package pgident parses and safely quotes PostgreSQL schema/table identifiers.
//
// Config keys may be written in SQL form (e.g. public."CamelCaseTable"). Callers
// that need raw catalog names must Parse first; callers that build SQL must
// Qualify raw names with pgx.Identifier.Sanitize — never Sanitize a string that
// already contains quote characters.
package pgident

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Parse splits a Postgres table reference into raw schema and table names.
// It understands unquoted identifiers, double-quoted identifiers (including
// "" escapes and dots inside quotes), and schema.table qualification.
//
// Unqualified references return an empty schema; callers apply their own
// default (e.g. "public" for catalog lookups).
func Parse(ref string) (schema, table string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("pgident: empty table reference")
	}

	parts, err := splitQualified(ref)
	if err != nil {
		return "", "", err
	}
	switch len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("pgident: empty identifier in %q", ref)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("pgident: too many dotted components in %q (use quotes for names containing dots)", ref)
	}
}

// Qualify returns a safely quoted schema.table (or just table when schema is
// empty) suitable for interpolating into SQL.
func Qualify(schema, table string) string {
	if schema != "" {
		return pgx.Identifier{schema, table}.Sanitize()
	}
	return pgx.Identifier{table}.Sanitize()
}

// splitQualified tokenizes a Postgres-style dotted identifier list, respecting
// double-quoted segments so dots inside quotes do not split components.
func splitQualified(ref string) ([]string, error) {
	var parts []string
	i := 0
	for i < len(ref) {
		part, next, err := readIdent(ref, i)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
		i = next
		if i >= len(ref) {
			break
		}
		if ref[i] != '.' {
			return nil, fmt.Errorf("pgident: unexpected character %q in %q", ref[i], ref)
		}
		i++ // skip dot
		if i >= len(ref) {
			return nil, fmt.Errorf("pgident: trailing dot in %q", ref)
		}
	}
	return parts, nil
}

// readIdent reads one identifier starting at i. Quoted identifiers keep their
// exact contents (with "" → "); unquoted identifiers are taken literally up to
// the next dot (Postgres folds unquoted names to lowercase at parse time in
// SQL, but config keys are already the intended catalog spelling).
func readIdent(ref string, i int) (name string, next int, err error) {
	if ref[i] == '"' {
		return readQuoted(ref, i)
	}
	start := i
	for i < len(ref) && ref[i] != '.' {
		if ref[i] == '"' {
			return "", 0, fmt.Errorf("pgident: quote mid-identifier in %q", ref)
		}
		i++
	}
	name = ref[start:i]
	if name == "" {
		return "", 0, fmt.Errorf("pgident: empty identifier in %q", ref)
	}
	return name, i, nil
}

func readQuoted(ref string, i int) (name string, next int, err error) {
	// Opening quote at i.
	i++
	var b strings.Builder
	for i < len(ref) {
		if ref[i] != '"' {
			b.WriteByte(ref[i])
			i++
			continue
		}
		// Closing quote, or "" escape.
		if i+1 < len(ref) && ref[i+1] == '"' {
			b.WriteByte('"')
			i += 2
			continue
		}
		return b.String(), i + 1, nil
	}
	return "", 0, fmt.Errorf("pgident: unclosed quote in %q", ref)
}
