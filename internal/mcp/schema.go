package mcp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/olucasandrade/kaptanto/internal/parser/pgoutput"
)

// ColumnInfo is one column in a get_table_schema result.
type ColumnInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	PrimaryKey bool   `json:"primary_key"`
	Redacted   bool   `json:"redacted,omitempty"`
}

// SchemaProvider supplies live relation metadata for MCP schema tools.
// Implementations must be safe for concurrent use. A nil provider means
// only configured tables are visible (captured=false; empty Postgres schema).
type SchemaProvider interface {
	// Captured returns qualified table names known from live source metadata.
	Captured() []string
	// Schema returns column definitions for a qualified table name.
	// ok=false when the table has not been observed yet.
	Schema(qualified string) (cols []ColumnInfo, ok bool)
}

// StaticSchemaProvider is an in-memory SchemaProvider for tests.
type StaticSchemaProvider struct {
	mu     sync.RWMutex
	tables map[string][]ColumnInfo // qualified name → columns
}

// NewStaticSchemaProvider builds a provider from a name→columns map.
func NewStaticSchemaProvider(tables map[string][]ColumnInfo) *StaticSchemaProvider {
	cp := make(map[string][]ColumnInfo, len(tables))
	for k, v := range tables {
		cols := make([]ColumnInfo, len(v))
		copy(cols, v)
		cp[k] = cols
	}
	return &StaticSchemaProvider{tables: cp}
}

// Captured implements SchemaProvider.
func (p *StaticSchemaProvider) Captured() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.tables))
	for name := range p.tables {
		out = append(out, name)
	}
	return out
}

// Schema implements SchemaProvider.
func (p *StaticSchemaProvider) Schema(qualified string) ([]ColumnInfo, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cols, ok := p.tables[qualified]
	if !ok {
		return nil, false
	}
	out := make([]ColumnInfo, len(cols))
	copy(out, cols)
	return out, true
}

// Set replaces or adds a table schema (tests).
func (p *StaticSchemaProvider) Set(qualified string, cols []ColumnInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tables == nil {
		p.tables = make(map[string][]ColumnInfo)
	}
	cp := make([]ColumnInfo, len(cols))
	copy(cp, cols)
	p.tables[qualified] = cp
}

// ParserSchemaProvider reads live schemas from a pgoutput.Parser's RelationCache.
type ParserSchemaProvider struct {
	Parser *pgoutput.Parser
}

// Captured implements SchemaProvider.
func (p *ParserSchemaProvider) Captured() []string {
	if p == nil || p.Parser == nil {
		return nil
	}
	cache := p.Parser.RelationCache()
	if cache == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, msg := range cache.All() {
		name := qualifiedRelation(msg)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// Schema implements SchemaProvider.
func (p *ParserSchemaProvider) Schema(qualified string) ([]ColumnInfo, bool) {
	if p == nil || p.Parser == nil {
		return nil, false
	}
	cache := p.Parser.RelationCache()
	if cache == nil {
		return nil, false
	}
	schema, table := splitQualified(qualified)
	msg, ok := cache.LookupByName(schema, table)
	if !ok {
		return nil, false
	}
	return columnsFromRelation(msg), true
}

func qualifiedRelation(msg *pglogrepl.RelationMessageV2) string {
	if msg.Namespace == "" {
		return msg.RelationName
	}
	return msg.Namespace + "." + msg.RelationName
}

func columnsFromRelation(msg *pglogrepl.RelationMessageV2) []ColumnInfo {
	out := make([]ColumnInfo, 0, len(msg.Columns))
	for _, c := range msg.Columns {
		if c == nil {
			continue
		}
		out = append(out, ColumnInfo{
			Name:       c.Name,
			Type:       oidTypeName(c.DataType),
			PrimaryKey: c.Flags&1 == 1,
		})
	}
	return out
}

// oidTypeName maps a Postgres type OID to a human-readable name.
func oidTypeName(oid uint32) string {
	if t, ok := pgtype.NewMap().TypeForOID(oid); ok {
		return t.Name
	}
	return fmt.Sprintf("oid:%d", oid)
}

// mergeTableNames returns the sorted-stable union of configured + captured names.
func mergeTableNames(configured, captured []string) []string {
	seen := make(map[string]struct{}, len(configured)+len(captured))
	out := make([]string, 0, len(configured)+len(captured))
	for _, name := range configured {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range captured {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
