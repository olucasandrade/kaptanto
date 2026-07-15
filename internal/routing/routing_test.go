package routing

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompile_EmptyConfig_MatchesAll(t *testing.T) {
	m, err := Compile(MatchConfig{})
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpInsert,
		After:     json.RawMessage(`{"id":1}`),
	}
	assert.True(t, m.Match(ev))
}

// --- Table glob tests ---

func TestCompile_Tables_ExactMatch(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"public.orders"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "users", Operation: event.OpInsert,
	}))
}

func TestCompile_Tables_SchemaWildcard(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"public.*"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "users", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "private", Table: "secrets", Operation: event.OpInsert,
	}))
}

func TestCompile_Tables_BareWildcard(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"*"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "other", Table: "anything", Operation: event.OpDelete,
	}))
}

func TestCompile_Tables_SuffixWildcard(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"*.orders"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "warehouse", Table: "orders", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "users", Operation: event.OpInsert,
	}))
}

func TestCompile_Tables_MultiplePatterns(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"public.orders", "public.users"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "users", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "products", Operation: event.OpInsert,
	}))
}

func TestCompile_Tables_MultiStarRejected(t *testing.T) {
	_, err := Compile(MatchConfig{Tables: []string{"*.*"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-star")
	assert.Contains(t, err.Error(), "*.*")
}

func TestCompile_Tables_EmptyPatternRejected(t *testing.T) {
	_, err := Compile(MatchConfig{Tables: []string{""}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tables")
}

func TestCompile_Tables_NoSchemaEvent(t *testing.T) {
	m, err := Compile(MatchConfig{Tables: []string{"orders"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Table: "orders", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
}

// --- Operation bitmask tests ---

func TestCompile_Operations_SingleOp(t *testing.T) {
	m, err := Compile(MatchConfig{Operations: []string{"insert"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpUpdate,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpDelete,
	}))
}

func TestCompile_Operations_MultipleOps(t *testing.T) {
	m, err := Compile(MatchConfig{Operations: []string{"insert", "update"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpUpdate,
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpDelete,
	}))
}

func TestCompile_Operations_AllOps(t *testing.T) {
	m, err := Compile(MatchConfig{Operations: []string{"insert", "update", "delete", "read"}})
	require.NoError(t, err)

	for _, op := range []event.Operation{event.OpInsert, event.OpUpdate, event.OpDelete, event.OpRead} {
		assert.True(t, m.Match(&event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: op,
		}))
	}
}

func TestCompile_Operations_EmptyMatchesAll(t *testing.T) {
	m, err := Compile(MatchConfig{})
	require.NoError(t, err)

	for _, op := range []event.Operation{event.OpInsert, event.OpUpdate, event.OpDelete, event.OpRead} {
		assert.True(t, m.Match(&event.ChangeEvent{
			Schema: "public", Table: "orders", Operation: op,
		}))
	}
}

func TestCompile_Operations_UnknownOpRejected(t *testing.T) {
	_, err := Compile(MatchConfig{Operations: []string{"truncate"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operations")
	assert.Contains(t, err.Error(), "truncate")
}

func TestCompile_Operations_CaseInsensitive(t *testing.T) {
	m, err := Compile(MatchConfig{Operations: []string{"INSERT", "Delete"}})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpDelete,
	}))
}

func TestCompile_Operations_ControlOpNotMatched(t *testing.T) {
	m, err := Compile(MatchConfig{})
	require.NoError(t, err)

	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpControl,
	}))
}

// --- WHERE tests ---

func TestCompile_Where_SimpleFilter(t *testing.T) {
	m, err := Compile(MatchConfig{Where: "status = 'active'"})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
		After: json.RawMessage(`{"status":"active"}`),
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
		After: json.RawMessage(`{"status":"archived"}`),
	}))
}

func TestCompile_Where_BeforePrefix(t *testing.T) {
	m, err := Compile(MatchConfig{Where: "before.status = 'active'"})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpUpdate,
		Before: json.RawMessage(`{"status":"active"}`),
		After:  json.RawMessage(`{"status":"archived"}`),
	}))
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpUpdate,
		Before: json.RawMessage(`{"status":"archived"}`),
		After:  json.RawMessage(`{"status":"active"}`),
	}))
}

func TestCompile_Where_InvalidExpressionRejected(t *testing.T) {
	_, err := Compile(MatchConfig{Where: "status LIKE '%foo%'"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "where")
}

func TestCompile_Where_EmptyAlwaysTrue(t *testing.T) {
	m, err := Compile(MatchConfig{Where: ""})
	require.NoError(t, err)

	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
	}))
}

// --- Composition tests (all three parts AND together) ---

func TestMatch_AllPartsANDTogether(t *testing.T) {
	m, err := Compile(MatchConfig{
		Tables:     []string{"public.orders"},
		Operations: []string{"insert"},
		Where:      "status = 'active'",
	})
	require.NoError(t, err)

	// All match
	assert.True(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
		After: json.RawMessage(`{"status":"active"}`),
	}))

	// Wrong table
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "users", Operation: event.OpInsert,
		After: json.RawMessage(`{"status":"active"}`),
	}))

	// Wrong operation
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpUpdate,
		After: json.RawMessage(`{"status":"active"}`),
	}))

	// WHERE fails
	assert.False(t, m.Match(&event.ChangeEvent{
		Schema: "public", Table: "orders", Operation: event.OpInsert,
		After: json.RawMessage(`{"status":"archived"}`),
	}))
}

// --- Error message quality tests ---

func TestCompile_ErrorMessages_NameField(t *testing.T) {
	tests := []struct {
		name    string
		mc      MatchConfig
		wantErr string
	}{
		{
			name:    "multi-star glob",
			mc:      MatchConfig{Tables: []string{"a*b*c"}},
			wantErr: "tables: multi-star pattern",
		},
		{
			name:    "unknown operation",
			mc:      MatchConfig{Operations: []string{"upsert"}},
			wantErr: "operations: unknown operation",
		},
		{
			name:    "invalid WHERE",
			mc:      MatchConfig{Where: "((missing close"},
			wantErr: "where:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.mc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// --- Glob unit tests ---

func TestTableGlob_Match(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"public.orders", "public.orders", true},
		{"public.orders", "public.users", false},
		{"public.*", "public.orders", true},
		{"public.*", "public.users", true},
		{"public.*", "private.orders", false},
		{"*", "anything", true},
		{"*", "public.orders", true},
		{"*.orders", "public.orders", true},
		{"*.orders", "warehouse.orders", true},
		{"*.orders", "public.users", false},
		{"pre*suf", "pre_middle_suf", true},
		{"pre*suf", "presuf", true},
		{"pre*suf", "pre_middle_other", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.input, func(t *testing.T) {
			g, err := compileTableGlob(tt.pattern)
			require.NoError(t, err)
			assert.Equal(t, tt.want, g.match(tt.input))
		})
	}
}
