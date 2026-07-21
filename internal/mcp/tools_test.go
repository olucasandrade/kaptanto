package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pglogrepl"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/parser/pgoutput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaTools_ListAndGetViaInProcessClient(t *testing.T) {
	t.Setenv("MCP_TOOLS_KEY", "schema-tools-secret")

	var auditBuf strings.Builder
	auditor := mcp.NewAuditorWriter(&auditBuf, slog.New(slog.NewTextHandler(io.Discard, nil)))

	provider := mcp.NewStaticSchemaProvider(map[string][]mcp.ColumnInfo{
		"public.orders": {
			{Name: "id", Type: "int4", PrimaryKey: true},
			{Name: "email", Type: "text"},
			{Name: "status", Type: "text"},
		},
		"public.users": {
			{Name: "id", Type: "int4", PrimaryKey: true},
			{Name: "name", Type: "text"},
		},
	})

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{
				Name:   "support",
				Key:    "${MCP_TOOLS_KEY}",
				Tables: []string{"public.orders"},
				Redact: []config.MCPRedactConfig{{
					Tables:  []string{"public.orders"},
					Columns: []string{"email"},
				}},
			}},
		},
		DataDir:          t.TempDir(),
		Auditor:          auditor,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		SourceType:       mcp.SourcePostgres,
		ConfiguredTables: []string{"public.orders", "public.users", "public.payments"},
		Schema:           provider,
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	defer func() { _ = s.Close() }()

	principal := s.Keys()[0]
	ctx := mcp.ContextWithPrincipal(context.Background(), principal)

	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	// list_tables: ACL filters to public.orders only; captured=true for orders.
	listRes, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_tables", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.False(t, listRes.IsError, "list_tables should succeed: %v", contentText(listRes))

	var listOut struct {
		Tables []mcp.TableEntry `json:"tables"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(listRes)), &listOut))
	require.Len(t, listOut.Tables, 1)
	assert.Equal(t, "public.orders", listOut.Tables[0].Name)
	assert.Equal(t, mcp.SourcePostgres, listOut.Tables[0].Source)
	assert.True(t, listOut.Tables[0].Captured)

	// get_table_schema: redacted column marked.
	schemaRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_table_schema",
		Arguments: map[string]any{"table": "public.orders"},
	})
	require.NoError(t, err)
	require.False(t, schemaRes.IsError)

	var schemaOut struct {
		Table   string           `json:"table"`
		Columns []mcp.ColumnInfo `json:"columns"`
		Source  string           `json:"source"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(schemaRes)), &schemaOut))
	assert.Equal(t, "public.orders", schemaOut.Table)
	assert.Equal(t, mcp.SourcePostgres, schemaOut.Source)
	require.Len(t, schemaOut.Columns, 3)
	byName := map[string]mcp.ColumnInfo{}
	for _, c := range schemaOut.Columns {
		byName[c.Name] = c
	}
	assert.True(t, byName["id"].PrimaryKey)
	assert.True(t, byName["email"].Redacted)
	assert.False(t, byName["status"].Redacted)

	// ACL deny on public.users.
	denyRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_table_schema",
		Arguments: map[string]any{"table": "public.users"},
	})
	require.NoError(t, err)
	require.True(t, denyRes.IsError)
	assert.Contains(t, contentText(denyRes), "table not accessible")

	audit := auditBuf.String()
	lines := nonEmptyLines(audit)
	require.Len(t, lines, 3, "exactly one audit line per call; got %q", audit)

	assert.Contains(t, lines[0], `"tool":"list_tables"`)
	assert.Contains(t, lines[0], `"outcome":"ok"`)
	assert.Contains(t, lines[1], `"tool":"get_table_schema"`)
	assert.Contains(t, lines[1], `"outcome":"ok"`)
	assert.Contains(t, lines[2], `"tool":"get_table_schema"`)
	assert.Contains(t, lines[2], `"outcome":"denied"`)
	assert.Contains(t, lines[2], `"tables":["public.users"]`)
	assert.NotContains(t, audit, "schema-tools-secret")
}

func TestSchemaTools_MongoDBSchemaless(t *testing.T) {
	t.Setenv("MCP_MONGO_KEY", "mongo-secret")

	var auditBuf strings.Builder
	auditor := mcp.NewAuditorWriter(&auditBuf, slog.New(slog.NewTextHandler(io.Discard, nil)))

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{
				Name:   "agent",
				Key:    "${MCP_MONGO_KEY}",
				Tables: []string{"app.orders"},
			}},
		},
		DataDir:          t.TempDir(),
		Auditor:          auditor,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		SourceType:       mcp.SourceMongoDB,
		ConfiguredTables: []string{"app.orders", "app.events"},
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	principal := s.Keys()[0]
	ctx := mcp.ContextWithPrincipal(context.Background(), principal)
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	listRes, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_tables", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.False(t, listRes.IsError)
	var listOut struct {
		Tables []mcp.TableEntry `json:"tables"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(listRes)), &listOut))
	require.Len(t, listOut.Tables, 1)
	assert.Equal(t, "app.orders", listOut.Tables[0].Name)
	assert.Equal(t, mcp.SourceMongoDB, listOut.Tables[0].Source)
	assert.False(t, listOut.Tables[0].Captured)

	schemaRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_table_schema",
		Arguments: map[string]any{"table": "app.orders"},
	})
	require.NoError(t, err)
	require.False(t, schemaRes.IsError)
	var schemaOut struct {
		Table   string           `json:"table"`
		Columns []mcp.ColumnInfo `json:"columns"`
		Source  string           `json:"source"`
		Note    string           `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(schemaRes)), &schemaOut))
	assert.Equal(t, mcp.SourceMongoDB, schemaOut.Source)
	assert.Empty(t, schemaOut.Columns)
	assert.Equal(t, "schemaless; fields observed per-event", schemaOut.Note)

	require.Len(t, nonEmptyLines(auditBuf.String()), 2)
}

func TestSchemaTools_ParserSchemaProvider(t *testing.T) {
	t.Setenv("MCP_PG_KEY", "pg-secret")

	parser := pgoutput.New("default", nil)
	parser.RelationCache().Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   42,
			Namespace:    "public",
			RelationName: "orders",
			Columns: []*pglogrepl.RelationMessageColumn{
				{Flags: 1, Name: "id", DataType: 23},
				{Flags: 0, Name: "email", DataType: 25},
			},
		},
	})

	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_PG_KEY}"}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir:          t.TempDir(),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		SourceType:       mcp.SourcePostgres,
		ConfiguredTables: []string{"public.orders"},
		Schema:           &mcp.ParserSchemaProvider{Parser: parser},
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	principal := s.Keys()[0]
	ctx := mcp.ContextWithPrincipal(context.Background(), principal)
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	schemaRes, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "get_table_schema",
		Arguments: map[string]any{"table": "public.orders"},
	})
	require.NoError(t, err)
	require.False(t, schemaRes.IsError)
	var schemaOut struct {
		Columns []mcp.ColumnInfo `json:"columns"`
	}
	require.NoError(t, json.Unmarshal([]byte(structuredOrText(schemaRes)), &schemaOut))
	require.Len(t, schemaOut.Columns, 2)
	assert.Equal(t, "int4", schemaOut.Columns[0].Type)
	assert.True(t, schemaOut.Columns[0].PrimaryKey)
	assert.Equal(t, "text", schemaOut.Columns[1].Type)
}

func TestSchemaTools_UnauthenticatedErrors(t *testing.T) {
	t.Setenv("MCP_UNAUTH", "tok")
	var auditBuf strings.Builder
	auditor := mcp.NewAuditorWriter(&auditBuf, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_UNAUTH}"}},
		},
		DataDir: t.TempDir(),
		Auditor: auditor,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	// Connect without principal in context.
	ctx := context.Background()
	cs := connectInProcess(t, ctx, s)
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_tables", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, contentText(res), "unauthenticated")
	require.Eventually(t, func() bool {
		return strings.Contains(auditBuf.String(), `"outcome":"error"`)
	}, time.Second, 10*time.Millisecond)
}

func connectInProcess(t testing.TB, ctx context.Context, s *mcp.Server) *sdk.ClientSession {
	t.Helper()
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := s.SDK().Connect(ctx, t1, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	return cs
}

func structuredOrText(res *sdk.CallToolResult) string {
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err == nil {
			return string(b)
		}
	}
	return contentText(res)
}

func contentText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
