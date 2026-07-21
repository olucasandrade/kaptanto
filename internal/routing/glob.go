package routing

import (
	"fmt"
	"strings"
)

// tableGlob is a compiled table-name glob pattern.
// It supports three forms:
//   - exact match (no wildcard)
//   - prefix match: "prefix*"
//   - suffix match: "*suffix"
//   - contains match: "prefix*suffix"
//   - bare wildcard: "*" (matches everything)
//
// Patterns with more than one '*' are rejected at compile time.
type tableGlob struct {
	// matchAll is true for the bare "*" pattern.
	matchAll bool
	// exact is the literal string for exact-match patterns.
	exact string
	// prefix is the part before '*' (empty for suffix-only or matchAll).
	prefix string
	// suffix is the part after '*' (empty for prefix-only or matchAll).
	suffix string
}

// compileTableGlob validates and compiles a single table pattern.
func compileTableGlob(pattern string) (tableGlob, error) {
	if pattern == "" {
		return tableGlob{}, fmt.Errorf("tables: empty pattern")
	}

	starCount := strings.Count(pattern, "*")
	if starCount == 0 {
		return tableGlob{exact: pattern}, nil
	}
	if starCount > 1 {
		return tableGlob{}, fmt.Errorf("tables: multi-star pattern %q not supported", pattern)
	}

	if pattern == "*" {
		return tableGlob{matchAll: true}, nil
	}

	idx := strings.IndexByte(pattern, '*')
	return tableGlob{
		prefix: pattern[:idx],
		suffix: pattern[idx+1:],
	}, nil
}

// match reports whether name matches this compiled glob.
func (g tableGlob) match(name string) bool {
	if g.matchAll {
		return true
	}
	if g.exact != "" {
		return name == g.exact
	}
	if len(name) < len(g.prefix)+len(g.suffix) {
		return false
	}
	return strings.HasPrefix(name, g.prefix) && strings.HasSuffix(name, g.suffix)
}
