package output_test

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/output"
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
// mustMatch evaluates f against ev and fails the test on error.
func mustMatch(t *testing.T, f *output.RowFilter, ev *event.ChangeEvent) bool {
	t.Helper()
	got, err := f.Match(ev)
	if err != nil {
		t.Fatalf("Match error: %v", err)
	}
	return got
}

func TestParseRowFilter_Empty(t *testing.T) {
	f, err := output.ParseRowFilter("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := makeEvent(event.OpInsert, "", `{"status":"anything"}`)
	got := mustMatch(t, f, ev)
	if !got {
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
		_, err := output.ParseRowFilter(expr)
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
		_, err := output.ParseRowFilter(expr)
		if err == nil {
			t.Errorf("expr %q: expected parse error but got nil", expr)
		}
	}
}

// TestRowFilter_MatchNotEqual verifies != comparison.
func TestRowFilter_MatchNotEqual(t *testing.T) {
	f, err := output.ParseRowFilter("status != 'cancelled'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("status=active should match status != 'cancelled'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"cancelled"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("status=cancelled should not match status != 'cancelled'")
	}
}

// TestRowFilter_MatchDeleteUsesBefore verifies that delete events evaluate against Before.
func TestRowFilter_MatchDeleteUsesBefore(t *testing.T) {
	f, err := output.ParseRowFilter("status != 'cancelled'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev := makeEvent(event.OpDelete, `{"status":"active"}`, "")
	got := mustMatch(t, f, ev)
	if !got {
		t.Error("delete event with Before.status=active should match status != 'cancelled'")
	}
}

// TestRowFilter_MatchIsNull verifies IS NULL evaluation.
func TestRowFilter_MatchIsNull(t *testing.T) {
	f, err := output.ParseRowFilter("col IS NULL")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"col":null}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("col=null should match 'col IS NULL'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"col":"x"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("col=x should not match 'col IS NULL'")
	}

	// Missing key is also considered null.
	ev3 := makeEvent(event.OpInsert, "", `{"other":"x"}`)
	got = mustMatch(t, f, ev3)
	if !got {
		t.Error("missing key should match 'col IS NULL'")
	}
}

// TestRowFilter_MatchIsNotNull verifies IS NOT NULL evaluation.
func TestRowFilter_MatchIsNotNull(t *testing.T) {
	f, err := output.ParseRowFilter("col IS NOT NULL")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"col":"x"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("col=x should match 'col IS NOT NULL'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"col":null}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("col=null should not match 'col IS NOT NULL'")
	}
}

// TestRowFilter_MatchIn verifies IN list evaluation.
func TestRowFilter_MatchIn(t *testing.T) {
	f, err := output.ParseRowFilter("status IN ('active','pending')")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("status=active should match IN ('active','pending')")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"cancelled"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("status=cancelled should not match IN ('active','pending')")
	}
}

// TestRowFilter_MatchAnd verifies AND evaluation.
func TestRowFilter_MatchAnd(t *testing.T) {
	f, err := output.ParseRowFilter("a = 'x' AND b = 'y'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x","b":"y"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("a=x AND b=y should match")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"x","b":"z"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("a=x AND b=z should not match")
	}
}

// TestRowFilter_MatchOr verifies OR evaluation.
func TestRowFilter_MatchOr(t *testing.T) {
	f, err := output.ParseRowFilter("a = 'z' OR a = 'x'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("a=x should match 'a = z OR a = x'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"other"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("a=other should not match 'a = z OR a = x'")
	}
}

// TestRowFilter_MatchNot verifies NOT evaluation.
func TestRowFilter_MatchNot(t *testing.T) {
	f, err := output.ParseRowFilter("NOT a = 'z'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"a":"x"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("a=x should match 'NOT a = z'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"a":"z"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("a=z should not match 'NOT a = z'")
	}
}

// TestRowFilter_MatchNumericComparison verifies numeric > comparison.
func TestRowFilter_MatchNumericComparison(t *testing.T) {
	f, err := output.ParseRowFilter("amount > 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":100}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("amount=100 should match 'amount > 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":25}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("amount=25 should not match 'amount > 50'")
	}
}

// TestRowFilter_MatchEqual verifies = comparison.
func TestRowFilter_MatchEqual(t *testing.T) {
	f, err := output.ParseRowFilter("status = 'active'")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"status":"active"}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("status=active should match 'status = active'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"status":"inactive"}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("status=inactive should not match 'status = active'")
	}
}

// TestRowFilter_MatchGreaterThanOrEqual verifies >= comparison.
func TestRowFilter_MatchGreaterThanOrEqual(t *testing.T) {
	f, err := output.ParseRowFilter("amount >= 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":50}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("amount=50 should match 'amount >= 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":49}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("amount=49 should not match 'amount >= 50'")
	}
}

// TestRowFilter_MatchLessThan verifies < comparison.
func TestRowFilter_MatchLessThan(t *testing.T) {
	f, err := output.ParseRowFilter("amount < 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":25}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("amount=25 should match 'amount < 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":75}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("amount=75 should not match 'amount < 50'")
	}
}

// TestRowFilter_MatchLessThanOrEqual verifies <= comparison.
func TestRowFilter_MatchLessThanOrEqual(t *testing.T) {
	f, err := output.ParseRowFilter("amount <= 50")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ev1 := makeEvent(event.OpInsert, "", `{"amount":50}`)
	got := mustMatch(t, f, ev1)
	if !got {
		t.Error("amount=50 should match 'amount <= 50'")
	}

	ev2 := makeEvent(event.OpInsert, "", `{"amount":51}`)
	got = mustMatch(t, f, ev2)
	if got {
		t.Error("amount=51 should not match 'amount <= 50'")
	}
}

// TestRowFilter_PrefixedColumns exercises the before./after. column prefix
// feature (G2-01) across insert, update, and delete operations.
func TestRowFilter_PrefixedColumns(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		op     event.Operation
		before string
		after  string
		want   bool
	}{
		// --- after. prefix ---
		{
			name:  "after.status on insert matches",
			expr:  "after.status = 'active'",
			op:    event.OpInsert,
			after: `{"status":"active"}`,
			want:  true,
		},
		{
			name:  "after.status on insert no match",
			expr:  "after.status = 'active'",
			op:    event.OpInsert,
			after: `{"status":"cancelled"}`,
			want:  false,
		},
		{
			name:   "after.status on update matches after side",
			expr:   "after.status = 'active'",
			op:     event.OpUpdate,
			before: `{"status":"pending"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},
		{
			name:   "after.status on update does not look at before",
			expr:   "after.status = 'pending'",
			op:     event.OpUpdate,
			before: `{"status":"pending"}`,
			after:  `{"status":"active"}`,
			want:   false,
		},
		{
			name:   "after.x on delete (After is nil) → false",
			expr:   "after.status = 'active'",
			op:     event.OpDelete,
			before: `{"status":"active"}`,
			want:   false,
		},

		// --- before. prefix ---
		{
			name:   "before.total on update matches before side",
			expr:   "before.total > 100",
			op:     event.OpUpdate,
			before: `{"total":200}`,
			after:  `{"total":50}`,
			want:   true,
		},
		{
			name:   "before.total on update no match",
			expr:   "before.total > 100",
			op:     event.OpUpdate,
			before: `{"total":50}`,
			after:  `{"total":200}`,
			want:   false,
		},
		{
			name:   "before.status on delete matches",
			expr:   "before.status = 'active'",
			op:     event.OpDelete,
			before: `{"status":"active"}`,
			want:   true,
		},
		{
			name:  "before.x on insert (Before is nil) → false",
			expr:  "before.status = 'active'",
			op:    event.OpInsert,
			after: `{"status":"active"}`,
			want:  false,
		},

		// --- IS NULL / IS NOT NULL with prefixes ---
		{
			name:   "after.embedding IS NOT NULL on delete → false (nil side)",
			expr:   "after.embedding IS NOT NULL",
			op:     event.OpDelete,
			before: `{"embedding":"vec"}`,
			want:   false,
		},
		{
			name:   "after.embedding IS NULL on delete → true (nil side treated as null)",
			expr:   "after.embedding IS NULL",
			op:     event.OpDelete,
			before: `{"embedding":"vec"}`,
			want:   true,
		},
		{
			name:  "before.x IS NOT NULL on insert → false (nil side)",
			expr:  "before.col IS NOT NULL",
			op:    event.OpInsert,
			after: `{"col":"val"}`,
			want:  false,
		},
		{
			name:  "before.x IS NULL on insert → true (nil side)",
			expr:  "before.col IS NULL",
			op:    event.OpInsert,
			after: `{"col":"val"}`,
			want:  true,
		},
		{
			name:   "after.col IS NOT NULL on update with value",
			expr:   "after.col IS NOT NULL",
			op:     event.OpUpdate,
			before: `{"col":null}`,
			after:  `{"col":"present"}`,
			want:   true,
		},
		{
			name:   "after.col IS NULL on update with null",
			expr:   "after.col IS NULL",
			op:     event.OpUpdate,
			before: `{"col":"present"}`,
			after:  `{"col":null}`,
			want:   true,
		},

		// --- IN with prefix ---
		{
			name:   "after.status IN list on update",
			expr:   "after.status IN ('active','pending')",
			op:     event.OpUpdate,
			before: `{"status":"cancelled"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},
		{
			name:   "before.status IN list on update no match",
			expr:   "before.status IN ('active','pending')",
			op:     event.OpUpdate,
			before: `{"status":"cancelled"}`,
			after:  `{"status":"active"}`,
			want:   false,
		},
		{
			name:   "after.x IN list on delete (nil side) → false",
			expr:   "after.status IN ('active','pending')",
			op:     event.OpDelete,
			before: `{"status":"active"}`,
			want:   false,
		},

		// --- Mixed: prefixed AND un-prefixed ---
		{
			name:   "mixed: after.status AND un-prefixed amount on update",
			expr:   "after.status = 'active' AND amount > 50",
			op:     event.OpUpdate,
			before: `{"status":"pending","amount":25}`,
			after:  `{"status":"active","amount":100}`,
			want:   true,
		},
		{
			name:   "mixed: before.status AND after.status on update",
			expr:   "before.status = 'pending' AND after.status = 'active'",
			op:     event.OpUpdate,
			before: `{"status":"pending"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},
		{
			name:   "mixed: before.status AND after.status mismatch",
			expr:   "before.status = 'active' AND after.status = 'active'",
			op:     event.OpUpdate,
			before: `{"status":"pending"}`,
			after:  `{"status":"active"}`,
			want:   false,
		},

		// --- Un-prefixed backward compatibility ---
		{
			name:  "un-prefixed on insert uses After",
			expr:  "status = 'active'",
			op:    event.OpInsert,
			after: `{"status":"active"}`,
			want:  true,
		},
		{
			name:   "un-prefixed on delete falls back to Before",
			expr:   "status = 'active'",
			op:     event.OpDelete,
			before: `{"status":"active"}`,
			want:   true,
		},
		{
			name:   "un-prefixed on update uses After",
			expr:   "status = 'active'",
			op:     event.OpUpdate,
			before: `{"status":"pending"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},

		// --- Numeric comparison with prefix ---
		{
			name:   "before.amount >= on update",
			expr:   "before.amount >= 100",
			op:     event.OpUpdate,
			before: `{"amount":100}`,
			after:  `{"amount":50}`,
			want:   true,
		},
		{
			name:   "after.amount < on update",
			expr:   "after.amount < 100",
			op:     event.OpUpdate,
			before: `{"amount":200}`,
			after:  `{"amount":50}`,
			want:   true,
		},

		// --- NOT with prefix ---
		{
			name:   "NOT after.status on update",
			expr:   "NOT after.status = 'cancelled'",
			op:     event.OpUpdate,
			before: `{"status":"cancelled"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},

		// --- != with prefix ---
		{
			name:   "after.status != on update",
			expr:   "after.status != 'cancelled'",
			op:     event.OpUpdate,
			before: `{"status":"cancelled"}`,
			after:  `{"status":"active"}`,
			want:   true,
		},

		// --- OR with prefix ---
		{
			name:   "after.status OR before.status",
			expr:   "after.status = 'active' OR before.status = 'active'",
			op:     event.OpUpdate,
			before: `{"status":"active"}`,
			after:  `{"status":"pending"}`,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := output.ParseRowFilter(tt.expr)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			ev := makeEvent(tt.op, tt.before, tt.after)
			got := mustMatch(t, f, ev)
			if err != nil {
				t.Fatalf("Match error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseRowFilter_PrefixedParsing verifies that prefixed column names
// parse successfully.
func TestParseRowFilter_PrefixedParsing(t *testing.T) {
	cases := []string{
		"before.status = 'active'",
		"after.status != 'cancelled'",
		"before.total > 100",
		"after.col IS NULL",
		"before.col IS NOT NULL",
		"after.status IN ('a','b')",
		"before.x = 'y' AND after.z = 'w'",
		"after.a = 'x' OR before.b = 'y'",
		"NOT after.status = 'deleted'",
	}
	for _, expr := range cases {
		_, err := output.ParseRowFilter(expr)
		if err != nil {
			t.Errorf("expr %q: unexpected parse error: %v", expr, err)
		}
	}
}
