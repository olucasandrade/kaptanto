package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolListTables     = "list_tables"
	toolGetTableSchema = "get_table_schema"

	errTableNotAccessible = "table not accessible"
	mongoSchemalessNote   = "schemaless; fields observed per-event"
)

// Source type labels returned by schema tools.
const (
	SourcePostgres = "postgres"
	SourceMongoDB  = "mongodb"
)

// listTablesInput is empty — list_tables takes no parameters.
type listTablesInput struct{}

// TableEntry is one row in a list_tables result.
type TableEntry struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Captured bool   `json:"captured"`
}

// listTablesOutput is the structured result of list_tables.
type listTablesOutput struct {
	Tables []TableEntry `json:"tables"`
}

// getTableSchemaInput is the argument object for get_table_schema.
type getTableSchemaInput struct {
	Table string `json:"table" jsonschema:"qualified table name, e.g. public.orders"`
}

// getTableSchemaOutput is the structured result of get_table_schema.
type getTableSchemaOutput struct {
	Table   string       `json:"table"`
	Columns []ColumnInfo `json:"columns"`
	Source  string       `json:"source"`
	Note    string       `json:"note,omitempty"`
}

// errDenied is returned (as a tool error) when ACL blocks access.
var errDenied = errors.New(errTableNotAccessible)

// ContextWithPrincipal attaches an authenticated API key to ctx (tests + HTTP).
func ContextWithPrincipal(ctx context.Context, k *ResolvedKey) context.Context {
	return context.WithValue(ctx, keyPrincipal, k)
}

// registerSchemaTools wires list_tables and get_table_schema onto the SDK server.
func (s *Server) registerSchemaTools() {
	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolListTables,
		Description: "List CDC tables visible to this API key (configured ∪ live relation metadata, ACL-filtered).",
	}, s.handleListTables)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        toolGetTableSchema,
		Description: "Return column schema for a table. Redacted columns are listed with redacted=true. ACL misses return an error.",
	}, s.handleGetTableSchema)
}

func (s *Server) handleListTables(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	_ listTablesInput,
) (*sdk.CallToolResult, listTablesOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}

	out, tables, outcome, err := s.listTables(key)
	s.RecordToolCall(keyName, toolListTables, []string{}, tables, outcome, time.Since(start))
	if err != nil {
		return nil, listTablesOutput{}, err
	}
	return nil, out, nil
}

func (s *Server) listTables(key *ResolvedKey) (listTablesOutput, []string, string, error) {
	if key == nil {
		return listTablesOutput{}, nil, OutcomeError, fmt.Errorf("unauthenticated")
	}

	var captured []string
	capturedSet := map[string]struct{}{}
	s.schemaMu.RLock()
	provider := s.schema
	s.schemaMu.RUnlock()
	if provider != nil {
		captured = provider.Captured()
		for _, n := range captured {
			capturedSet[n] = struct{}{}
		}
	}

	all := mergeTableNames(s.configuredTables, captured)
	allowed := key.ACL.FilterTables(all)

	src := s.sourceType
	if src == "" {
		src = SourcePostgres
	}

	tables := make([]TableEntry, 0, len(allowed))
	for _, name := range allowed {
		_, isCaptured := capturedSet[name]
		tables = append(tables, TableEntry{
			Name:     name,
			Source:   src,
			Captured: isCaptured,
		})
	}
	return listTablesOutput{Tables: tables}, allowed, OutcomeOK, nil
}

func (s *Server) handleGetTableSchema(
	ctx context.Context,
	_ *sdk.CallToolRequest,
	in getTableSchemaInput,
) (*sdk.CallToolResult, getTableSchemaOutput, error) {
	start := time.Now()
	key := PrincipalFromContext(ctx)
	keyName := "unknown"
	if key != nil {
		keyName = key.Name
	}

	out, outcome, err := s.getTableSchema(key, in.Table)
	params := []string{"table"}
	var auditTables []string
	if in.Table != "" {
		auditTables = []string{in.Table}
	}
	s.RecordToolCall(keyName, toolGetTableSchema, params, auditTables, outcome, time.Since(start))
	if err != nil {
		return nil, getTableSchemaOutput{}, err
	}
	return nil, out, nil
}

func (s *Server) getTableSchema(key *ResolvedKey, table string) (getTableSchemaOutput, string, error) {
	if key == nil {
		return getTableSchemaOutput{}, OutcomeError, fmt.Errorf("unauthenticated")
	}
	table = strings.TrimSpace(table)
	if table == "" {
		return getTableSchemaOutput{}, OutcomeError, fmt.Errorf("table is required")
	}
	if !key.ACL.AllowTable(table) {
		return getTableSchemaOutput{}, OutcomeDenied, errDenied
	}

	src := s.sourceType
	if src == "" {
		src = SourcePostgres
	}

	if src == SourceMongoDB {
		return getTableSchemaOutput{
			Table:   table,
			Columns: []ColumnInfo{},
			Source:  SourceMongoDB,
			Note:    mongoSchemalessNote,
		}, OutcomeOK, nil
	}

	var cols []ColumnInfo
	s.schemaMu.RLock()
	provider := s.schema
	s.schemaMu.RUnlock()
	if provider != nil {
		if got, ok := provider.Schema(table); ok {
			cols = got
		}
	}
	for i := range cols {
		cols[i].Redacted = key.ACL.IsColumnRedacted(table, cols[i].Name)
	}
	return getTableSchemaOutput{
		Table:   table,
		Columns: cols,
		Source:  SourcePostgres,
	}, OutcomeOK, nil
}
