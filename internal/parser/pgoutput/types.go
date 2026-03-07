// Package pgoutput parses pgoutput logical replication messages into ChangeEvents.
// It requires no live Postgres connection — all decoding is pure byte manipulation.
package pgoutput

import (
	"github.com/jackc/pglogrepl"
)

// toastKey is the cache key for TOASTCache entries.
// It is keyed by (RelationID, PK) where PK is the JSON-marshaled primary key value.
type toastKey struct {
	RelationID uint32
	PK         string
}

// decodeColumns converts pglogrepl TupleData columns into a Go map using the
// relation schema for column names.
//
// prevRow is used to fill unchanged TOAST values ('u'). If prevRow is nil,
// TOAST columns are omitted (first-seen TOAST with no cache entry).
//
//   - 'n' (null)  → nil value in map
//   - 'u' (TOAST) → copy value from prevRow[colName] if prevRow != nil; omit otherwise
//   - 't' (text)  → string(col.Data)
//   - 'b' (binary)→ col.Data ([]byte)
func decodeColumns(rel *pglogrepl.RelationMessageV2, cols []*pglogrepl.TupleDataColumn, prevRow map[string]any) map[string]any {
	row := make(map[string]any, len(cols))
	for i, col := range cols {
		if i >= len(rel.Columns) {
			break
		}
		name := rel.Columns[i].Name
		switch col.DataType {
		case pglogrepl.TupleDataTypeNull:
			row[name] = nil
		case pglogrepl.TupleDataTypeToast:
			if prevRow != nil {
				if v, ok := prevRow[name]; ok {
					row[name] = v
				}
				// if not in prevRow, omit (no cached value)
			}
			// if prevRow is nil, omit entirely
		case pglogrepl.TupleDataTypeText:
			row[name] = string(col.Data)
		case pglogrepl.TupleDataTypeBinary:
			row[name] = col.Data
		}
	}
	return row
}

// extractPK identifies primary key columns (RelationMessageColumn.Flags & 1 == 1)
// and returns a map of pk column names → decoded values from the tuple.
// Only text-encoded values are supported (standard pgoutput mode).
func extractPK(rel *pglogrepl.RelationMessageV2, cols []*pglogrepl.TupleDataColumn) map[string]any {
	pk := make(map[string]any)
	for i, col := range cols {
		if i >= len(rel.Columns) {
			break
		}
		relCol := rel.Columns[i]
		if relCol.Flags&1 == 1 {
			switch col.DataType {
			case pglogrepl.TupleDataTypeText:
				pk[relCol.Name] = string(col.Data)
			case pglogrepl.TupleDataTypeNull:
				pk[relCol.Name] = nil
			}
		}
	}
	return pk
}
