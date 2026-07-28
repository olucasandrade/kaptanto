package mcp

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACL_AllowTable_Globs(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "support",
		Tables: []string{"public.orders", "public.tickets"},
	})
	require.NoError(t, err)

	assert.True(t, acl.AllowTable("public.orders"))
	assert.True(t, acl.AllowTable("public.tickets"))
	assert.False(t, acl.AllowTable("public.users"))
	assert.False(t, acl.AllowTable("other.orders"))
}

func TestACL_AllowTable_SchemaGlob(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "ops",
		Tables: []string{"public.*"},
	})
	require.NoError(t, err)
	assert.True(t, acl.AllowTable("public.orders"))
	assert.True(t, acl.AllowTable("public.users"))
	assert.False(t, acl.AllowTable("billing.orders"))
}

func TestACL_AllowTable_StarMeansAll(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{Name: "admin", Tables: []string{"*"}})
	require.NoError(t, err)
	assert.True(t, acl.AllowTable("public.anything"))
	assert.True(t, acl.AllowTable("x.y"))
}

func TestCompileACL_EmptyTablesFails(t *testing.T) {
	_, err := CompileACL(config.MCPAPIKey{Name: "admin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tables is required")
}

func TestACL_Apply_RedactsAIContext(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "support",
		Tables: []string{"public.orders"},
		Redact: []config.MCPRedactConfig{{
			Tables:  []string{"public.orders"},
			Columns: []string{"email"},
		}},
	})
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpUpdate,
		After:     json.RawMessage(`{"id":1,"email":"a@b.c"}`),
		AIContext: json.RawMessage(`{"email":"a@b.c","intent":"fulfill"}`),
	}
	out, ok := acl.Apply(ev)
	require.True(t, ok)
	require.NotNil(t, out)
	assert.Nil(t, out.AIContext)
	assert.Contains(t, string(out.After), redactedValue)
	assert.Contains(t, string(ev.AIContext), "a@b.c")
}

func TestACL_FilterTables(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "support",
		Tables: []string{"public.orders"},
	})
	require.NoError(t, err)
	got := acl.FilterTables([]string{"public.orders", "public.users", "public.orders"})
	assert.Equal(t, []string{"public.orders", "public.orders"}, got)
}

func TestACL_Apply_RedactsColumns(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "support",
		Tables: []string{"public.orders"},
		Redact: []config.MCPRedactConfig{{
			Tables:  []string{"public.orders"},
			Columns: []string{"email", "card_last4"},
		}},
	})
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		Schema:    "public",
		Table:     "orders",
		Operation: event.OpUpdate,
		Before:    json.RawMessage(`{"id":1,"email":"a@b.c","card_last4":"4242","status":"open"}`),
		After:     json.RawMessage(`{"id":1,"email":"a@b.c","card_last4":"4242","status":"paid"}`),
	}
	out, ok := acl.Apply(ev)
	require.True(t, ok)
	require.NotNil(t, out)

	var before, after map[string]any
	require.NoError(t, json.Unmarshal(out.Before, &before))
	require.NoError(t, json.Unmarshal(out.After, &after))

	assert.Equal(t, float64(1), before["id"])
	assert.Equal(t, "open", before["status"])
	assert.Equal(t, redactedValue, before["email"])
	assert.Equal(t, redactedValue, before["card_last4"])
	assert.Equal(t, redactedValue, after["email"])
	assert.Equal(t, "paid", after["status"])

	// Input not mutated.
	assert.Contains(t, string(ev.After), "a@b.c")
}

func TestACL_Apply_DeniedTable(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "support",
		Tables: []string{"public.orders"},
	})
	require.NoError(t, err)
	out, ok := acl.Apply(&event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpRead})
	assert.False(t, ok)
	assert.Nil(t, out)
}

func TestACL_IsColumnRedacted(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "k",
		Tables: []string{"*"},
		Redact: []config.MCPRedactConfig{{
			Tables:  []string{"public.*"},
			Columns: []string{"ssn"},
		}},
	})
	require.NoError(t, err)
	assert.True(t, acl.IsColumnRedacted("public.users", "ssn"))
	assert.False(t, acl.IsColumnRedacted("public.users", "name"))
	assert.False(t, acl.IsColumnRedacted("billing.users", "ssn"))
}

func TestACL_EmptyRedactColumnsSkipped(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "k",
		Tables: []string{"*"},
		Redact: []config.MCPRedactConfig{
			{Tables: []string{"public.*"}, Columns: nil},
			{Tables: []string{"public.orders"}, Columns: []string{"email"}},
		},
	})
	require.NoError(t, err)
	assert.True(t, acl.IsColumnRedacted("public.orders", "email"))
}

func TestACL_MaskMalformedAndUnchanged(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{
		Name:   "k",
		Tables: []string{"*"},
		Redact: []config.MCPRedactConfig{{Columns: []string{"missing"}}},
	})
	require.NoError(t, err)
	ev := &event.ChangeEvent{
		Table:     "t",
		Operation: event.OpRead,
		After:     json.RawMessage(`{"id":1}`),
		Before:    json.RawMessage(`not-json`),
	}
	out, ok := acl.Apply(ev)
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`{"id":1}`), out.After)
	assert.Equal(t, json.RawMessage(`not-json`), out.Before)
}
