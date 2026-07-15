package routing_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompile_EmptyConfig_MatchesAll(t *testing.T) {
	m, err := routing.Compile(routing.MatchConfig{})
	require.NoError(t, err)
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpInsert,
	}))
}

func TestCompile_TablesFilter(t *testing.T) {
	m, err := routing.Compile(routing.MatchConfig{
		Tables: []string{"public.orders", "public.users"},
	})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert}))
	assert.True(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpDelete}))
	assert.False(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "products", Operation: event.OpInsert}))
}

func TestCompile_OperationsFilter(t *testing.T) {
	m, err := routing.Compile(routing.MatchConfig{
		Operations: []string{"insert", "update"},
	})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{Table: "any", Operation: event.OpInsert}))
	assert.True(t, m.Match(&event.ChangeEvent{Table: "any", Operation: event.OpUpdate}))
	assert.False(t, m.Match(&event.ChangeEvent{Table: "any", Operation: event.OpDelete}))
}

func TestCompile_CombinedFilter(t *testing.T) {
	m, err := routing.Compile(routing.MatchConfig{
		Tables:     []string{"public.orders"},
		Operations: []string{"insert"},
	})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert}))
	assert.False(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpUpdate}))
	assert.False(t, m.Match(&event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpInsert}))
}

func TestCompile_InvalidOperation(t *testing.T) {
	_, err := routing.Compile(routing.MatchConfig{
		Operations: []string{"upsert"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation")
}

func TestCompile_EmptyTableName(t *testing.T) {
	_, err := routing.Compile(routing.MatchConfig{
		Tables: []string{"public.orders", ""},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty table name")
}

func TestMatcher_NilEvent(t *testing.T) {
	m, _ := routing.Compile(routing.MatchConfig{})
	assert.False(t, m.Match(nil))
}

func TestMatchAll(t *testing.T) {
	m := routing.MatchAll()
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema:    "public",
		Table:     "anything",
		Operation: event.OpDelete,
	}))
}

func BenchmarkMatchDeliver_Miss(b *testing.B) {
	m, _ := routing.Compile(routing.MatchConfig{
		Tables:     []string{"public.orders"},
		Operations: []string{"insert"},
	})
	e := &event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpUpdate}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(e)
	}
}

func BenchmarkMatchDeliver_Hit(b *testing.B) {
	m, _ := routing.Compile(routing.MatchConfig{
		Tables:     []string{"public.orders"},
		Operations: []string{"insert"},
	})
	e := &event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(e)
	}
}
