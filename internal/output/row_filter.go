// Package output provides shared utilities for kaptanto output consumers.
// This file implements WHERE-expression row filtering (CFG-06).
//
// Supported grammar:
//
//	expr       = or_expr
//	or_expr    = and_expr ("OR" and_expr)*
//	and_expr   = not_expr ("AND" not_expr)*
//	not_expr   = "NOT" not_expr | primary
//	primary    = comparison | "(" expr ")"
//	comparison = column op value
//	           | column "IS" "NULL"
//	           | column "IS" "NOT" "NULL"
//	           | column "IN" "(" value_list ")"
//	op         = "=" | "!=" | ">" | "<" | ">=" | "<="
//	value      = string_literal | number_literal | null_literal
//
// Parse errors are returned at config-load time so invalid expressions are
// caught before any event is processed.
package output

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// RowFilter evaluates a WHERE expression against a ChangeEvent row.
// Use ParseRowFilter to construct one.
type RowFilter struct {
	root exprNode // nil means no-op (always match)
}

// ParseRowFilter compiles a WHERE expression string into a RowFilter.
// Returns an error if the expression is syntactically invalid or uses
// unsupported grammar, so that the problem is caught at startup, not at
// event-delivery time.
//
// An empty expression produces a no-op RowFilter that always returns true.
func ParseRowFilter(expr string) (*RowFilter, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return &RowFilter{}, nil
	}

	tokens, err := tokenize(expr)
	if err != nil {
		return nil, fmt.Errorf("row filter tokenize: %w", err)
	}

	p := &whereParser{tokens: tokens}
	root, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("row filter parse: %w", err)
	}
	if !p.done() {
		return nil, fmt.Errorf("row filter parse: unexpected token %q after expression", p.peek().value)
	}
	return &RowFilter{root: root}, nil
}

// Match evaluates the filter expression against the event.
//
// Semantic: the expression is evaluated against After. When After is nil
// (delete events), Before is used instead. If both are nil, Match returns
// true (no data to filter on).
//
// A no-op RowFilter (from ParseRowFilter("")) always returns true.
func (f *RowFilter) Match(ev *event.ChangeEvent) bool {
	if f.root == nil {
		return true
	}

	// Use After; fall back to Before for delete events (After is nil).
	raw := ev.After
	if raw == nil {
		raw = ev.Before
	}
	if raw == nil {
		return true
	}

	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		// Malformed JSON — let it through; this is not a filter error.
		return true
	}

	return f.root.test(row)
}

// ---------------------------------------------------------------------------
// AST node types
// ---------------------------------------------------------------------------

// exprNode is the common interface for all AST nodes.
// test() evaluates the node against a JSON row map.
type exprNode interface {
	test(row map[string]any) bool
}

// orNode evaluates left OR right.
type orNode struct{ left, right exprNode }

func (n *orNode) test(row map[string]any) bool {
	return n.left.test(row) || n.right.test(row)
}

// andNode evaluates left AND right.
type andNode struct{ left, right exprNode }

func (n *andNode) test(row map[string]any) bool {
	return n.left.test(row) && n.right.test(row)
}

// notNode evaluates NOT child.
type notNode struct{ child exprNode }

func (n *notNode) test(row map[string]any) bool {
	return !n.child.test(row)
}

// compareNode evaluates column op value.
type compareNode struct {
	column string
	op     string
	value  any // string or float64
}

func (n *compareNode) test(row map[string]any) bool {
	colVal := row[n.column]
	return compareValues(colVal, n.op, n.value)
}

// isNullNode evaluates column IS NULL or column IS NOT NULL.
type isNullNode struct {
	column  string
	notNull bool // true = IS NOT NULL
}

func (n *isNullNode) test(row map[string]any) bool {
	val, exists := row[n.column]
	isNull := !exists || val == nil
	if n.notNull {
		return !isNull
	}
	return isNull
}

// inNode evaluates column IN (v1, v2, ...).
type inNode struct {
	column string
	values []any // string or float64
}

func (n *inNode) test(row map[string]any) bool {
	colVal := row[n.column]
	for _, v := range n.values {
		if compareValues(colVal, "=", v) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Value comparison
// ---------------------------------------------------------------------------

// compareValues compares a JSON row value against a parsed literal value.
// JSON numbers arrive as float64; string literals compared to float64 attempt
// numeric coercion so "amount > 50" works when the parsed literal is float64(50).
func compareValues(colVal any, op string, litVal any) bool {
	// Attempt numeric comparison first when both sides are (or can be) numbers.
	colFloat, colIsFloat := toFloat64(colVal)
	litFloat, litIsFloat := toFloat64(litVal)

	if colIsFloat && litIsFloat {
		switch op {
		case "=":
			return colFloat == litFloat
		case "!=":
			return colFloat != litFloat
		case ">":
			return colFloat > litFloat
		case "<":
			return colFloat < litFloat
		case ">=":
			return colFloat >= litFloat
		case "<=":
			return colFloat <= litFloat
		}
	}

	// Fall back to string comparison.
	colStr := anyToString(colVal)
	litStr := anyToString(litVal)
	switch op {
	case "=":
		return colStr == litStr
	case "!=":
		return colStr != litStr
	case ">":
		return colStr > litStr
	case "<":
		return colStr < litStr
	case ">=":
		return colStr >= litStr
	case "<=":
		return colStr <= litStr
	}
	return false
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", v)
}

// ---------------------------------------------------------------------------
// Tokenizer
// ---------------------------------------------------------------------------

type tokenKind int

const (
	tokIdent  tokenKind = iota // identifier or keyword
	tokString                  // 'string literal'
	tokNumber                  // numeric literal
	tokOp                      // =, !=, >, <, >=, <=
	tokLParen                  // (
	tokRParen                  // )
	tokComma                   // ,
	tokEOF
)

type wToken struct {
	kind  tokenKind
	value string
}

func tokenize(s string) ([]wToken, error) {
	var tokens []wToken
	i := 0
	for i < len(s) {
		// Skip whitespace.
		if unicode.IsSpace(rune(s[i])) {
			i++
			continue
		}

		switch {
		case s[i] == '\'':
			// String literal.
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string literal at position %d", i)
			}
			tokens = append(tokens, wToken{tokString, s[i+1 : j]})
			i = j + 1

		case s[i] == '(':
			tokens = append(tokens, wToken{tokLParen, "("})
			i++

		case s[i] == ')':
			tokens = append(tokens, wToken{tokRParen, ")"})
			i++

		case s[i] == ',':
			tokens = append(tokens, wToken{tokComma, ","})
			i++

		case s[i] == '!' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, wToken{tokOp, "!="})
			i += 2

		case s[i] == '>' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, wToken{tokOp, ">="})
			i += 2

		case s[i] == '<' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, wToken{tokOp, "<="})
			i += 2

		case s[i] == '>':
			tokens = append(tokens, wToken{tokOp, ">"})
			i++

		case s[i] == '<':
			tokens = append(tokens, wToken{tokOp, "<"})
			i++

		case s[i] == '=':
			tokens = append(tokens, wToken{tokOp, "="})
			i++

		case unicode.IsDigit(rune(s[i])) || (s[i] == '-' && i+1 < len(s) && unicode.IsDigit(rune(s[i+1]))):
			// Numeric literal.
			j := i
			if s[j] == '-' {
				j++
			}
			for j < len(s) && (unicode.IsDigit(rune(s[j])) || s[j] == '.') {
				j++
			}
			tokens = append(tokens, wToken{tokNumber, s[i:j]})
			i = j

		case unicode.IsLetter(rune(s[i])) || s[i] == '_':
			// Identifier or keyword.
			j := i
			for j < len(s) && (unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] == '_') {
				j++
			}
			tokens = append(tokens, wToken{tokIdent, s[i:j]})
			i = j

		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", s[i], i)
		}
	}
	tokens = append(tokens, wToken{tokEOF, ""})
	return tokens, nil
}

// ---------------------------------------------------------------------------
// Recursive-descent parser
// ---------------------------------------------------------------------------

type whereParser struct {
	tokens []wToken
	pos    int
}

func (p *whereParser) peek() wToken {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return wToken{tokEOF, ""}
}

func (p *whereParser) consume() wToken {
	t := p.peek()
	p.pos++
	return t
}

func (p *whereParser) done() bool {
	return p.peek().kind == tokEOF
}

// isKeyword returns true if the current token is the given keyword
// (comparison is case-insensitive).
func (p *whereParser) isKeyword(kw string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.value, kw)
}

func (p *whereParser) parseExpr() (exprNode, error) {
	return p.parseOrExpr()
}

func (p *whereParser) parseOrExpr() (exprNode, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("OR") {
		p.consume()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = &orNode{left, right}
	}
	return left, nil
}

func (p *whereParser) parseAndExpr() (exprNode, error) {
	left, err := p.parseNotExpr()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("AND") {
		p.consume()
		right, err := p.parseNotExpr()
		if err != nil {
			return nil, err
		}
		left = &andNode{left, right}
	}
	return left, nil
}

func (p *whereParser) parseNotExpr() (exprNode, error) {
	if p.isKeyword("NOT") {
		p.consume()
		child, err := p.parseNotExpr()
		if err != nil {
			return nil, err
		}
		return &notNode{child}, nil
	}
	return p.parsePrimary()
}

func (p *whereParser) parsePrimary() (exprNode, error) {
	// Parenthesised sub-expression.
	if p.peek().kind == tokLParen {
		p.consume() // consume '('
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("expected ')' but got %q", p.peek().value)
		}
		p.consume() // consume ')'
		return inner, nil
	}

	// Must be an identifier (column name).
	col := p.peek()
	if col.kind != tokIdent {
		return nil, fmt.Errorf("expected column name but got %q", col.value)
	}

	// Guard against bare keywords used as unsupported functions/clauses.
	upper := strings.ToUpper(col.value)
	if isReservedClause(upper) {
		return nil, fmt.Errorf("unsupported SQL clause %q", col.value)
	}

	p.consume()

	// column IS [NOT] NULL
	if p.isKeyword("IS") {
		p.consume()
		notNull := false
		if p.isKeyword("NOT") {
			p.consume()
			notNull = true
		}
		if !p.isKeyword("NULL") {
			return nil, fmt.Errorf("expected NULL after IS [NOT], got %q", p.peek().value)
		}
		p.consume()
		return &isNullNode{column: col.value, notNull: notNull}, nil
	}

	// column IN (v1, v2, ...)
	if p.isKeyword("IN") {
		p.consume()
		if p.peek().kind != tokLParen {
			return nil, fmt.Errorf("expected '(' after IN, got %q", p.peek().value)
		}
		p.consume() // consume '('
		var values []any
		for {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			values = append(values, v)
			if p.peek().kind == tokComma {
				p.consume()
				continue
			}
			break
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("expected ')' after IN list, got %q", p.peek().value)
		}
		p.consume() // consume ')'
		return &inNode{column: col.value, values: values}, nil
	}

	// column op value
	if p.peek().kind != tokOp {
		return nil, fmt.Errorf("expected operator after column %q, got %q", col.value, p.peek().value)
	}
	op := p.consume().value

	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &compareNode{column: col.value, op: op, value: v}, nil
}

// parseValue parses a string literal, number literal, or NULL.
func (p *whereParser) parseValue() (any, error) {
	t := p.peek()
	switch t.kind {
	case tokString:
		p.consume()
		return t.value, nil
	case tokNumber:
		p.consume()
		f, err := strconv.ParseFloat(t.value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", t.value, err)
		}
		return f, nil
	case tokIdent:
		if strings.EqualFold(t.value, "NULL") {
			p.consume()
			return nil, nil
		}
		return nil, fmt.Errorf("expected value but got identifier %q", t.value)
	default:
		return nil, fmt.Errorf("expected value but got %q", t.value)
	}
}

// isReservedClause returns true for SQL clause keywords that this parser
// does not support, so they can be rejected at parse time.
func isReservedClause(upper string) bool {
	switch upper {
	case "BETWEEN", "LIKE", "ILIKE", "EXISTS", "CASE", "WHEN",
		"THEN", "ELSE", "END", "SELECT", "FROM", "WHERE",
		"GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET",
		"JOIN", "INNER", "LEFT", "RIGHT", "OUTER", "CROSS",
		"UNION", "INTERSECT", "EXCEPT":
		return true
	}
	return false
}
