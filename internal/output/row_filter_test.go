package output_test

import (
	"encoding/json"
	"testing"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/oklog/ulid/v2"
)

// makeEvent is a test helper that builds a minimal ChangeEvent.
func makeEvent(op event.Operation, before, after string) *event.ChangeEvent {
	ev := &event.ChangeEvent{
		ID:        ulid.ULID{},
		Operation: op,
		Table:     "test_table",
	}
	if before != "" {
		ev.Before = json.RawMessage(before)
	}
	if after != "" {
		ev.After = json.RawMessage(after)
	}
	return ev
}

// TestParseRowFilter_Empty verifies that an empty expression produces a no-op filter.
func TestParseRowFilter_Empty(t *testing.T) {
	f, err := ParseRowFilter("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := makeEvent(event.OpInsert, "", `{"status":"anything"}`)
	if !f.Match(ev) {
		t.Error("empty filter should always match")
	}
}

// TestParseRowFilter_ValidExpr verifies that valid expressions parse without error.
func TestParseRowFilter_ValidExpr(t *testing.T) {
	cases := []string{
		"status != 'cancelled'",
		"amount > 50",
		"col IS NULL",
		"col IS NOT NULL",
		"status IN ('active','pending')",
		"a = 'x' AND b = 'y'",
		"a = 'z' OR a = 'x'",
		"NOT a = 'z'",
	}
	for _, expr := range cases {
		_, err := ParseRowFilter(expr)
		if err != nil {
			t.Errorf("expr %q: unexpected parse error: %v", expr, err)
		}
	}
}

// TestParseRowFilter_InvalidExpr verifies that unsupported grammar returns an error at parse time.
func TestParseRowFilter_InvalidExpr(t *testing.T) {
	cases := []string{
		"UNSUPPORTED FUNC()",
		"BETWEEN 1 AND 10",
		"LIKE '%foo%'",
	}
	for _, expr := range cases {
		_, err := ParseRowFilter(expr)
		if err == nil {
			t.Errorf("expr %q: expected parse error but got nil", expr)
		}
	}
}

// TestRowFilter_MatchNotEqual verifies != comparison.
func TestRowFilter_MatchNotEqual(t *testing.T) {
	f, err := ParseRowFilter("status != 'cancelled'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	if !f.Match(ev1) {
		t.Error("status=active should match status != 'cancelled'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"cancelled"}`)
	if f.Match(ev2) {
		t.Error("status=cancelled should not match status != 'cancelled'")
	}
}

// TestRowFilter_MatchDeleteUsesBefore verifies that delete events evaluate against Before.
func TestRowFilter_MatchDeleteUsesBefore(t *testing.T) {
	f, err := ParseRowFilter("status != 'cancelled'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev := makeEvent(event.OpDelete, `{"status":"active"}`, "")
	if !f.Match(ev) {
		t.Error("delete event with Before.status=active should match status != 'cancelled'")
	}
}

// TestRowFilter_MatchIsNull verifies IS NULL evaluation.
func TestRowFilter_MatchIsNull(t *testing.T) {
	f, err := ParseRowFilter("col IS NULL")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"col":null}`)
	if !f.Match(ev1) {
		t.Error("col=null should match 'col IS NULL'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"col":"x"}`)
	if f.Match(ev2) {
		t.Error("col=x should not match 'col IS NULL'")
	}

	// Missing key is also considered null.
	ev3 := makeEvent(event.OpInsert, "", `{"other":"x"}`)
	if !f.Match(ev3) {
		t.Error("missing key should match 'col IS NULL'")
	}
}

// TestRowFilter_MatchIsNotNull verifies IS NOT NULL evaluation.
func TestRowFilter_MatchIsNotNull(t *testing.T) {
	f, err := ParseRowFilter("col IS NOT NULL")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"col":"x"}`)
	if !f.Match(ev1) {
		t.Error("col=x should match 'col IS NOT NULL'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"col":null}`)
	if f.Match(ev2) {
		t.Error("col=null should not match 'col IS NOT NULL'")
	}
}

// TestRowFilter_MatchIn verifies IN list evaluation.
func TestRowFilter_MatchIn(t *testing.T) {
	f, err := ParseRowFilter("status IN ('active','pending')")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	if !f.Match(ev1) {
		t.Error("status=active should match IN ('active','pending')")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"cancelled"}`)
	if f.Match(ev2) {
		t.Error("status=cancelled should not match IN ('active','pending')")
	}
}

// TestRowFilter_MatchAnd verifies AND evaluation.
func TestRowFilter_MatchAnd(t *testing.T) {
	f, err := ParseRowFilter("a = 'x' AND b = 'y'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x","b":"y"}`)
	if !f.Match(ev1) {
		t.Error("a=x AND b=y should match")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"x","b":"z"}`)
	if f.Match(ev2) {
		t.Error("a=x AND b=z should not match")
	}
}

// TestRowFilter_MatchOr verifies OR evaluation.
func TestRowFilter_MatchOr(t *testing.T) {
	f, err := ParseRowFilter("a = 'z' OR a = 'x'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x"}`)
	if !f.Match(ev1) {
		t.Error("a=x should match 'a = z OR a = x'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"other"}`)
	if f.Match(ev2) {
		t.Error("a=other should not match 'a = z OR a = x'")
	}
}

// TestRowFilter_MatchNot verifies NOT evaluation.
func TestRowFilter_MatchNot(t *testing.T) {
	f, err := ParseRowFilter("NOT a = 'z'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x"}`)
	if !f.Match(ev1) {
		t.Error("a=x should match 'NOT a = z'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"z"}`)
	if f.Match(ev2) {
		t.Error("a=z should not match 'NOT a = z'")
	}
}

// TestRowFilter_MatchNumericComparison verifies numeric > comparison.
func TestRowFilter_MatchNumericComparison(t *testing.T) {
	f, err := ParseRowFilter("amount > 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":100}`)
	if !f.Match(ev1) {
		t.Error("amount=100 should match 'amount > 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":25}`)
	if f.Match(ev2) {
		t.Error("amount=25 should not match 'amount > 50'")
	}
}

// TestRowFilter_MatchEqual verifies = comparison.
func TestRowFilter_MatchEqual(t *testing.T) {
	f, err := ParseRowFilter("status = 'active'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	if !f.Match(ev1) {
		t.Error("status=active should match 'status = active'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"inactive"}`)
	if f.Match(ev2) {
		t.Error("status=inactive should not match 'status = active'")
	}
}

// TestRowFilter_MatchGreaterThanOrEqual verifies >= comparison.
func TestRowFilter_MatchGreaterThanOrEqual(t *testing.T) {
	f, err := ParseRowFilter("amount >= 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":50}`)
	if !f.Match(ev1) {
		t.Error("amount=50 should match 'amount >= 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":49}`)
	if f.Match(ev2) {
		t.Error("amount=49 should not match 'amount >= 50'")
	}
}

// TestRowFilter_MatchLessThan verifies < comparison.
func TestRowFilter_MatchLessThan(t *testing.T) {
	f, err := ParseRowFilter("amount < 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":25}`)
	if !f.Match(ev1) {
		t.Error("amount=25 should match 'amount < 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":75}`)
	if f.Match(ev2) {
		t.Error("amount=75 should not match 'amount < 50'")
	}
}

// TestRowFilter_MatchLessThanOrEqual verifies <= comparison.
func TestRowFilter_MatchLessThanOrEqual(t *testing.T) {
	f, err := ParseRowFilter("amount <= 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":50}`)
	if !f.Match(ev1) {
		t.Error("amount=50 should match 'amount <= 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":51}`)
	if f.Match(ev2) {
		t.Error("amount=51 should not match 'amount <= 50'")
	}
}
