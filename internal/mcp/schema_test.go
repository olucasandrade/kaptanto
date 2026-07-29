package mcp

import (
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/parser/pgoutput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParserSchemaProvider_CapturedAndSchema(t *testing.T) {
	assert.Nil(t, (*ParserSchemaProvider)(nil).Captured())
	assert.Nil(t, (&ParserSchemaProvider{}).Captured())
	cols, ok := (&ParserSchemaProvider{}).Schema("public.x")
	assert.False(t, ok)
	assert.Nil(t, cols)

	parser := pgoutput.New("default", nil)
	p := &ParserSchemaProvider{Parser: parser}
	assert.Empty(t, p.Captured())

	parser.RelationCache().Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   7,
			Namespace:    "",
			RelationName: "bare",
			Columns: []*pglogrepl.RelationMessageColumn{
				nil,
				{Flags: 1, Name: "id", DataType: 23},
				{Flags: 0, Name: "mystery", DataType: 999999}, // unknown oid
			},
		},
	})
	parser.RelationCache().Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   8,
			Namespace:    "public",
			RelationName: "orders",
			Columns: []*pglogrepl.RelationMessageColumn{
				{Flags: 0, Name: "status", DataType: 25},
			},
		},
	})

	captured := p.Captured()
	assert.ElementsMatch(t, []string{"bare", "public.orders"}, captured)

	got, ok := p.Schema("bare")
	require.True(t, ok)
	require.Len(t, got, 2)
	assert.Equal(t, "int4", got[0].Type)
	assert.True(t, got[0].PrimaryKey)
	assert.Equal(t, "oid:999999", got[1].Type)

	_, ok = p.Schema("public.missing")
	assert.False(t, ok)
}

func TestStaticSchemaProvider_SetAndMiss(t *testing.T) {
	p := NewStaticSchemaProvider(nil)
	_, ok := p.Schema("public.x")
	assert.False(t, ok)
	p.Set("public.x", []ColumnInfo{{Name: "id", Type: "int4", PrimaryKey: true}})
	got, ok := p.Schema("public.x")
	require.True(t, ok)
	assert.Equal(t, "id", got[0].Name)
	assert.Equal(t, []string{"public.x"}, p.Captured())
}

func TestMergeTableNames(t *testing.T) {
	got := mergeTableNames(
		[]string{" public.a ", "public.b", "", "public.a"},
		[]string{"public.b", " public.c ", ""},
	)
	assert.Equal(t, []string{"public.a", "public.b", "public.c"}, got)
}

func TestServer_SetSchemaProvider(t *testing.T) {
	t.Setenv("MCP_SET_SCHEMA", "tok")
	s, err := New(Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_SET_SCHEMA}", Tables: []string{"*"}}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir:          t.TempDir(),
		ConfiguredTables: []string{"public.orders"},
		SourceType:       SourcePostgres,
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	provider := NewStaticSchemaProvider(map[string][]ColumnInfo{
		"public.orders": {{Name: "id", Type: "int4", PrimaryKey: true}},
	})
	s.SetSchemaProvider(provider)

	out, tables, outcome, err := s.listTables(s.Keys()[0])
	require.NoError(t, err)
	assert.Equal(t, OutcomeOK, outcome)
	require.Len(t, out.Tables, 1)
	assert.True(t, out.Tables[0].Captured)
	assert.Equal(t, []string{"public.orders"}, tables)
}

func TestGetTableSchema_EmptyTableError(t *testing.T) {
	acl, err := CompileACL(config.MCPAPIKey{Name: "k", Tables: []string{"*"}})
	require.NoError(t, err)
	s := &Server{sourceType: SourcePostgres, keys: []*ResolvedKey{{Name: "k", ACL: acl}}}
	_, outcome, err := s.getTableSchema(s.keys[0], "  ")
	assert.Equal(t, OutcomeError, outcome)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
