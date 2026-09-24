// Package event defines the unified ChangeEvent type used throughout the kaptanto pipeline.
// Every database change, regardless of source, is represented as a ChangeEvent.
package event

import (
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"
)

// Operation represents the type of database change.
type Operation string

const (
	// OpInsert is emitted when a row is inserted.
	OpInsert Operation = "insert"
	// OpUpdate is emitted when a row is updated.
	OpUpdate Operation = "update"
	// OpDelete is emitted when a row is deleted.
	OpDelete Operation = "delete"
	// OpRead is emitted for snapshot reads during backfills.
	OpRead Operation = "read"
	// OpControl is emitted for pipeline lifecycle signals (e.g., begin, commit, heartbeat).
	OpControl Operation = "control"
)

// ChangeEvent is the unified event format for all database changes.
//
// Before and After are always present in the JSON output:
//   - Inserts: Before is null, After is populated.
//   - Deletes: Before is populated, After is null.
//   - Updates: Both Before and After are populated.
//   - Reads (snapshot): Before is null, After is populated.
//
// Neither Before nor After uses omitempty; a nil json.RawMessage serializes as JSON null,
// which gives consumers a consistent schema regardless of operation type.
type ChangeEvent struct {
	// ID is a time-ordered ULID. Use IDGenerator.New() to produce IDs.
	ID ulid.ULID `json:"id"`

	// IdempotencyKey uniquely identifies this event for deduplication.
	// Format: "<source>:<schema>.<table>:<pk>:<op>:<position>"
	IdempotencyKey string `json:"idempotency_key"`

	// Timestamp is the wall-clock time when the change was captured.
	Timestamp time.Time `json:"timestamp"`

	// Source is the database connection identifier (e.g., DSN or logical name).
	Source string `json:"source"`

	// Operation is the type of change (insert, update, delete, read, control).
	Operation Operation `json:"operation"`

	// Database is the database name. Omitted when not applicable.
	Database string `json:"database,omitempty"`

	// Schema is the schema/namespace. Omitted when not applicable.
	Schema string `json:"schema,omitempty"`

	// Table is the table or collection name.
	Table string `json:"table"`

	// Key contains the primary key column(s) as a JSON object.
	Key json.RawMessage `json:"key"`

	// Before is the row state before the change. Null for inserts and snapshot reads.
	Before json.RawMessage `json:"before"`

	// After is the row state after the change. Null for deletes.
	After json.RawMessage `json:"after"`

	// Metadata contains source-specific fields (e.g., LSN, checkpoint, snapshot flag).
	Metadata map[string]any `json:"metadata"`

	// AIContext carries optional AI-generated metadata attached by the enrichment
	// stage. Kaptanto treats it as opaque JSON; the documented shape:
	// {intent?: string, entities?: [{type, value, field?}], suggested_actions?: [string],
	//  embedding?: {model: string, vector: [float...]}, custom?: object}
	AIContext json.RawMessage `json:"ai_context,omitempty"`

	// Cached decoded row maps for RowFilter / Matcher evaluation. Not serialized.
	// Parsed at most once per event; not safe for concurrent first-parse races
	// on the same *ChangeEvent (delivery paths are single-threaded per event).
	filterBefore     map[string]any
	filterAfter      map[string]any
	filterRowsParsed bool
	filterRowsErr    error
	qualifiedTable   string // schema.table cache for routing matchers
}

// DecodedRows returns Before/After as maps, parsing JSON at most once.
func (e *ChangeEvent) DecodedRows() (before, after map[string]any, err error) {
	if e.filterRowsParsed {
		return e.filterBefore, e.filterAfter, e.filterRowsErr
	}
	e.filterRowsParsed = true
	if e.Before != nil {
		if uerr := json.Unmarshal(e.Before, &e.filterBefore); uerr != nil {
			e.filterRowsErr = uerr
			return nil, nil, uerr
		}
	}
	if e.After != nil {
		if uerr := json.Unmarshal(e.After, &e.filterAfter); uerr != nil {
			e.filterRowsErr = uerr
			return nil, nil, uerr
		}
	}
	return e.filterBefore, e.filterAfter, nil
}

// QualifiedTable returns schema.table (or table alone when schema is empty),
// caching the string so multi-matcher evaluation does not re-allocate.
func (e *ChangeEvent) QualifiedTable() string {
	if e.qualifiedTable != "" {
		return e.qualifiedTable
	}
	if e.Schema == "" {
		e.qualifiedTable = e.Table
	} else {
		e.qualifiedTable = e.Schema + "." + e.Table
	}
	return e.qualifiedTable
}
