package routing

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// BenchmarkMatch50Matchers_Miss verifies RTG-02: 50 matchers over one event
// must stay < 1ms and produce zero allocations on the miss path.
func BenchmarkMatch50Matchers_Miss(b *testing.B) {
	matchers := make([]*Matcher, 50)
	for i := range matchers {
		m, err := Compile(MatchConfig{
			Tables:     []string{"other_schema.table_" + string(rune('a'+i%26))},
			Operations: []string{"insert"},
			Where:      "status = 'active'",
		})
		if err != nil {
			b.Fatal(err)
		}
		matchers[i] = m
	}

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpUpdate,
		After:     json.RawMessage(`{"status":"archived","id":123}`),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, m := range matchers {
			m.Match(ev)
		}
	}
}

// BenchmarkMatch50Matchers_Hit benchmarks the hit path (for reference).
func BenchmarkMatch50Matchers_Hit(b *testing.B) {
	matchers := make([]*Matcher, 50)
	for i := range matchers {
		m, err := Compile(MatchConfig{
			Tables:     []string{"public.*"},
			Operations: []string{"insert", "update", "delete", "read"},
			Where:      "id > 0",
		})
		if err != nil {
			b.Fatal(err)
		}
		matchers[i] = m
	}

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpInsert,
		After:     json.RawMessage(`{"id":123,"status":"active"}`),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, m := range matchers {
			m.Match(ev)
		}
	}
}

// TestAllocsPerRun_MissPath asserts zero allocations on the miss path (RTG-02).
func TestAllocsPerRun_MissPath(t *testing.T) {
	matchers := make([]*Matcher, 50)
	for i := range matchers {
		m, err := Compile(MatchConfig{
			Tables:     []string{"other_schema.table_" + string(rune('a'+i%26))},
			Operations: []string{"insert"},
		})
		if err != nil {
			t.Fatal(err)
		}
		matchers[i] = m
	}

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpUpdate,
		After:     json.RawMessage(`{"status":"archived","id":123}`),
	}

	allocs := testing.AllocsPerRun(100, func() {
		for _, m := range matchers {
			m.Match(ev)
		}
	})
	if allocs != 0 {
		t.Fatalf("RTG-02 violated: expected 0 allocs on miss path, got %f", allocs)
	}
}
