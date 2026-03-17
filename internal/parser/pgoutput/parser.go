package pgoutput

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/kaptanto/kaptanto/internal/event"
)

// Parser converts raw pgoutput WAL bytes into *event.ChangeEvent values.
//
// It maintains a RelationCache (schema metadata) and a TOASTCache (for merging
// unchanged TOAST column values in UPDATE events).
//
// Parse returns (nil, nil) for Begin, Commit, and Relation messages — these
// update internal state but do not produce ChangeEvents.
//
// Parse returns (nil, error) for DML messages referencing an unknown RelationID.
type Parser struct {
	sourceID  string
	idGen     *event.IDGenerator
	relations *RelationCache
	toast     *TOASTCache
	currentLSN pglogrepl.LSN
}

// New creates a Parser for the given source identifier.
// sourceID is embedded in every ChangeEvent's Source and IdempotencyKey fields.
func New(sourceID string, idGen *event.IDGenerator) *Parser {
	return &Parser{
		sourceID:  sourceID,
		idGen:     idGen,
		relations: NewRelationCache(),
		toast:     NewTOASTCache(),
	}
}

// Parse decodes a raw pgoutput WAL message and returns a ChangeEvent.
//
// inStream must be true when inside a streaming transaction (after
// StreamStartMessageV2 and before StreamStopMessageV2). For standard
// non-streaming logical replication, pass false.
func (p *Parser) Parse(walData []byte, inStream bool) (*event.ChangeEvent, error) {
	msg, err := pglogrepl.ParseV2(walData, inStream)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: parse WAL bytes: %w", err)
	}

	switch m := msg.(type) {
	case *pglogrepl.BeginMessage:
		p.currentLSN = m.FinalLSN
		return nil, nil

	case *pglogrepl.CommitMessage:
		return nil, nil

	case *pglogrepl.RelationMessageV2:
		p.relations.Set(m)
		return nil, nil

	case *pglogrepl.InsertMessageV2:
		return p.handleInsert(m)

	case *pglogrepl.UpdateMessageV2:
		return p.handleUpdate(m)

	case *pglogrepl.DeleteMessageV2:
		return p.handleDelete(m)

	default:
		// Truncate, Type, Origin, streaming control messages — no ChangeEvent.
		return nil, nil
	}
}

// handleInsert processes an InsertMessageV2.
func (p *Parser) handleInsert(m *pglogrepl.InsertMessageV2) (*event.ChangeEvent, error) {
	rel, ok := p.relations.Get(m.RelationID)
	if !ok {
		return nil, fmt.Errorf("pgoutput: unknown relation ID %d", m.RelationID)
	}

	pkMap := extractPK(rel, m.Tuple.Columns)
	pkStr := marshalPK(pkMap)
	afterJSON, err := decodeAndSerializeRow(rel, m.Tuple.Columns, nil)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: decode/serialize after-row (insert): %w", err)
	}
	// Update TOAST cache with decoded row for future TOAST merge.
	// Pure-Go path: decodeAndSerializeRow is not yet Rust-backed for TOAST;
	// we still maintain p.toast for the !rust path compatibility.
	// Under rust tag, TOAST cache is managed separately (Plan 10-03 wires it).
	tk := toastKey{RelationID: m.RelationID, PK: pkStr}
	if row := decodeColumns(rel, m.Tuple.Columns, nil); row != nil {
		p.toast.Set(tk, row)
	}

	keyJSON, err := json.Marshal(pkMap)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: marshal key: %w", err)
	}

	ev := p.newEvent(rel, event.OpInsert, pkStr, keyJSON, nil, afterJSON)
	return ev, nil
}

// handleUpdate processes an UpdateMessageV2.
func (p *Parser) handleUpdate(m *pglogrepl.UpdateMessageV2) (*event.ChangeEvent, error) {
	rel, ok := p.relations.Get(m.RelationID)
	if !ok {
		return nil, fmt.Errorf("pgoutput: unknown relation ID %d", m.RelationID)
	}

	// For TOAST merge: look up the cached row by primary key from new tuple.
	pkMap := extractPK(rel, m.NewTuple.Columns)
	pkStr := marshalPK(pkMap)
	tk := toastKey{RelationID: m.RelationID, PK: pkStr}

	var prevRow map[string]any
	if cached, found := p.toast.Get(tk); found {
		prevRow = cached
	}

	afterJSON, err := decodeAndSerializeRow(rel, m.NewTuple.Columns, prevRow)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: decode/serialize after-row (update): %w", err)
	}
	// Update TOAST cache with the decoded row for future TOAST merges.
	if row := decodeColumns(rel, m.NewTuple.Columns, prevRow); row != nil {
		p.toast.Set(tk, row)
	}

	keyJSON, err := json.Marshal(pkMap)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: marshal key: %w", err)
	}

	var beforeJSON json.RawMessage
	if m.OldTuple != nil {
		oldRow := decodeColumns(rel, m.OldTuple.Columns, nil)
		var merr error
		beforeJSON, merr = json.Marshal(oldRow)
		if merr != nil {
			return nil, fmt.Errorf("pgoutput: marshal before-row (update): %w", merr)
		}
	}
	ev := p.newEvent(rel, event.OpUpdate, pkStr, keyJSON, beforeJSON, afterJSON)
	return ev, nil
}

// handleDelete processes a DeleteMessageV2.
func (p *Parser) handleDelete(m *pglogrepl.DeleteMessageV2) (*event.ChangeEvent, error) {
	rel, ok := p.relations.Get(m.RelationID)
	if !ok {
		return nil, fmt.Errorf("pgoutput: unknown relation ID %d", m.RelationID)
	}

	pkMap := extractPK(rel, m.OldTuple.Columns)
	pkStr := marshalPK(pkMap)
	tk := toastKey{RelationID: m.RelationID, PK: pkStr}
	p.toast.Delete(tk)

	keyJSON, err := json.Marshal(pkMap)
	if err != nil {
		return nil, fmt.Errorf("pgoutput: marshal key: %w", err)
	}

	var beforeJSON json.RawMessage
	if m.OldTuple != nil {
		oldRow := decodeColumns(rel, m.OldTuple.Columns, nil)
		var merr error
		beforeJSON, merr = json.Marshal(oldRow)
		if merr != nil {
			return nil, fmt.Errorf("pgoutput: marshal before-row (delete): %w", merr)
		}
	}
	ev := p.newEvent(rel, event.OpDelete, pkStr, keyJSON, beforeJSON, nil)
	return ev, nil
}

// newEvent builds a ChangeEvent with the idempotency key and metadata.
func (p *Parser) newEvent(
	rel *pglogrepl.RelationMessageV2,
	op event.Operation,
	pkStr string,
	keyJSON json.RawMessage,
	beforeJSON json.RawMessage,
	afterJSON json.RawMessage,
) *event.ChangeEvent {
	lsn := p.currentLSN
	idempotencyKey := fmt.Sprintf("%s:%s.%s:%s:%s:%s",
		p.sourceID,
		rel.Namespace,
		rel.RelationName,
		pkStr,
		string(op),
		lsn.String(),
	)

	return &event.ChangeEvent{
		ID:             p.idGen.New(),
		IdempotencyKey: idempotencyKey,
		Timestamp:      time.Now(),
		Source:         p.sourceID,
		Operation:      op,
		Schema:         rel.Namespace,
		Table:          rel.RelationName,
		Key:            keyJSON,
		Before:         beforeJSON,
		After:          afterJSON,
		Metadata: map[string]any{
			"lsn":      lsn.String(),
			"snapshot": false,
		},
	}
}

// ClearRelationCache resets the relation cache. It must be called at the start
// of every new replication session — Postgres re-sends RelationMessages at the
// beginning of each session, so stale OID → schema mappings must be evicted.
func (p *Parser) ClearRelationCache() {
	p.relations = NewRelationCache()
}

// marshalPK JSON-marshals the primary key map into a compact string.
// Returns "{}" on marshal failure (should not happen with string/nil values).
func marshalPK(pkMap map[string]any) string {
	b, err := json.Marshal(pkMap)
	if err != nil {
		return "{}"
	}
	return string(b)
}
