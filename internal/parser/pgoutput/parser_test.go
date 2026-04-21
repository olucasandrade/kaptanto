// Package pgoutput_test tests the pgoutput WAL parser without a live Postgres connection.
// All pglogrepl message structs are constructed directly in tests.
package pgoutput_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/parser/pgoutput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers to build raw WAL byte slices ---

// encodeRelation builds a pgoutput Relation message byte slice.
// Wire format after the type byte:
//
//	uint32 RelationID | string Namespace\0 | string RelationName\0 | uint8 ReplicaIdentity | uint16 ColumnNum
//	then for each column: uint8 Flags | string Name\0 | uint32 DataType | int32 TypeModifier
func encodeRelation(relID uint32, ns, name string, cols []pglogrepl.RelationMessageColumn) []byte {
	var buf []byte
	buf = append(buf, 'R') // type byte
	buf = appendUint32(buf, relID)
	buf = appendString(buf, ns)
	buf = appendString(buf, name)
	buf = append(buf, 'd') // ReplicaIdentity DEFAULT
	buf = appendUint16(buf, uint16(len(cols)))
	for _, c := range cols {
		buf = append(buf, c.Flags)
		buf = appendString(buf, c.Name)
		buf = appendUint32(buf, c.DataType)
		buf = appendInt32(buf, c.TypeModifier)
	}
	return buf
}

// encodeInsert builds a pgoutput Insert message byte slice.
// Wire format: uint32 RelationID | 'N' | TupleData
func encodeInsert(relID uint32, cols []tupleCol) []byte {
	var buf []byte
	buf = append(buf, 'I')
	buf = appendUint32(buf, relID)
	buf = append(buf, 'N') // new tuple marker
	buf = encodeTuple(buf, cols)
	return buf
}

// encodeUpdate builds an Update message (no OldTuple — REPLICA IDENTITY DEFAULT).
func encodeUpdate(relID uint32, newCols []tupleCol) []byte {
	var buf []byte
	buf = append(buf, 'U')
	buf = appendUint32(buf, relID)
	buf = append(buf, 'N') // new tuple marker
	buf = encodeTuple(buf, newCols)
	return buf
}

// encodeDelete builds a Delete message (key tuple only).
func encodeDelete(relID uint32, keyCols []tupleCol) []byte {
	var buf []byte
	buf = append(buf, 'D')
	buf = appendUint32(buf, relID)
	buf = append(buf, 'K') // key tuple marker
	buf = encodeTuple(buf, keyCols)
	return buf
}

// encodeBegin builds a Begin message.
// Wire format: uint64 FinalLSN | uint64 CommitTime | uint32 Xid
func encodeBegin(lsn pglogrepl.LSN) []byte {
	var buf []byte
	buf = append(buf, 'B')
	buf = appendUint64(buf, uint64(lsn))
	buf = appendUint64(buf, 0) // CommitTime (microseconds since 2000-01-01)
	buf = appendUint32(buf, 1) // Xid
	return buf
}

// encodeCommit builds a Commit message.
func encodeCommit(lsn pglogrepl.LSN) []byte {
	var buf []byte
	buf = append(buf, 'C')
	buf = append(buf, 0)           // Flags
	buf = appendUint64(buf, uint64(lsn)) // CommitLSN
	buf = appendUint64(buf, uint64(lsn)) // TransactionEndLSN
	buf = appendUint64(buf, 0)     // CommitTime
	return buf
}

type tupleCol struct {
	dataType byte   // 'n', 'u', 't', 'b'
	data     []byte // only used for 't' and 'b'
}

func textCol(s string) tupleCol { return tupleCol{dataType: 't', data: []byte(s)} }
func nullCol() tupleCol         { return tupleCol{dataType: 'n'} }
func toastCol() tupleCol        { return tupleCol{dataType: 'u'} }

func encodeTuple(buf []byte, cols []tupleCol) []byte {
	buf = appendUint16(buf, uint16(len(cols)))
	for _, c := range cols {
		buf = append(buf, c.dataType)
		if c.dataType == 't' || c.dataType == 'b' {
			buf = appendUint32(buf, uint32(len(c.data)))
			buf = append(buf, c.data...)
		}
	}
	return buf
}

func appendUint16(buf []byte, v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return append(buf, b...)
}

func appendUint32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return append(buf, b...)
}

func appendInt32(buf []byte, v int32) []byte {
	return appendUint32(buf, uint32(v))
}

func appendUint64(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return append(buf, b...)
}

func appendString(buf []byte, s string) []byte {
	buf = append(buf, []byte(s)...)
	buf = append(buf, 0) // null terminator
	return buf
}

// --- shared test relation ---

var (
	testRelID     = uint32(12345)
	testNamespace = "public"
	testRelName   = "orders"
	testCols      = []pglogrepl.RelationMessageColumn{
		{Flags: 1, Name: "id", DataType: 23, TypeModifier: -1},       // pk, int4
		{Flags: 0, Name: "name", DataType: 25, TypeModifier: -1},     // text
		{Flags: 0, Name: "content", DataType: 25, TypeModifier: -1},  // text (TOAST-able)
	}
)

func newParser() *pgoutput.Parser {
	return pgoutput.New("main-pg", event.NewIDGenerator())
}

// sendRelation encodes and parses a Relation message, asserting no error and nil event.
func sendRelation(t *testing.T, p *pgoutput.Parser, relID uint32, ns, name string, cols []pglogrepl.RelationMessageColumn) {
	t.Helper()
	raw := encodeRelation(relID, ns, name, cols)
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	assert.Nil(t, ev, "Relation message must not produce a ChangeEvent")
}

// sendBegin encodes and parses a Begin message.
func sendBegin(t *testing.T, p *pgoutput.Parser, lsn pglogrepl.LSN) {
	t.Helper()
	raw := encodeBegin(lsn)
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	assert.Nil(t, ev)
}

// ---

// TestRelationUpdatesCache verifies that parsing a RelationMessage populates the
// RelationCache and that a subsequent Insert uses the cached schema.
func TestRelationUpdatesCache(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	raw := encodeInsert(testRelID, []tupleCol{textCol("42"), textCol("Acme"), textCol("big text")})
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, testRelName, ev.Table)
	assert.Equal(t, testNamespace, ev.Schema)
}

// TestInsertProducesChangeEvent verifies the Insert path fully.
func TestInsertProducesChangeEvent(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	raw := encodeInsert(testRelID, []tupleCol{textCol("1"), textCol("Widget"), textCol("desc")})
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpInsert, ev.Operation)
	assert.Equal(t, testRelName, ev.Table)
	assert.Equal(t, testNamespace, ev.Schema)
	assert.Nil(t, []byte(ev.Before), "Before must be nil for inserts")

	var after map[string]any
	require.NoError(t, json.Unmarshal(ev.After, &after))
	assert.Equal(t, "1", after["id"])
	assert.Equal(t, "Widget", after["name"])
	assert.Equal(t, "desc", after["content"])

	var key map[string]any
	require.NoError(t, json.Unmarshal(ev.Key, &key))
	assert.Equal(t, "1", key["id"])
}

// TestUpdateWithTOASTMerge verifies that an Update with an unchanged TOAST column
// ('u') correctly merges the cached value from a prior Insert.
func TestUpdateWithTOASTMerge(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	// Insert to populate TOAST cache.
	rawInsert := encodeInsert(testRelID, []tupleCol{
		textCol("10"), textCol("Original"), textCol("large cached content"),
	})
	_, err := p.Parse(rawInsert, false)
	require.NoError(t, err)

	// Update: id unchanged, name changed, content is TOAST (unchanged).
	rawUpdate := encodeUpdate(testRelID, []tupleCol{
		textCol("10"), textCol("Updated"), toastCol(),
	})
	ev, err := p.Parse(rawUpdate, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpUpdate, ev.Operation)

	var after map[string]any
	require.NoError(t, json.Unmarshal(ev.After, &after))
	assert.Equal(t, "10", after["id"])
	assert.Equal(t, "Updated", after["name"])
	// TOAST merge: content must be the cached value from the prior Insert.
	assert.Equal(t, "large cached content", after["content"])
}

// TestDeleteEvictsTOASTCache verifies Delete operation and cache eviction.
func TestDeleteEvictsTOASTCache(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	// Insert to populate TOAST cache.
	rawInsert := encodeInsert(testRelID, []tupleCol{
		textCol("99"), textCol("ToDelete"), textCol("cached"),
	})
	_, err := p.Parse(rawInsert, false)
	require.NoError(t, err)

	// Delete (key columns only — id is the PK).
	rawDelete := encodeDelete(testRelID, []tupleCol{
		textCol("99"), nullCol(), nullCol(),
	})
	ev, err := p.Parse(rawDelete, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpDelete, ev.Operation)
	assert.Nil(t, []byte(ev.After), "After must be nil for deletes")

	// After delete, TOAST cache entry should be evicted.
	// A subsequent update with TOAST should NOT merge the old value.
	rawUpdate := encodeUpdate(testRelID, []tupleCol{
		textCol("99"), textCol("Ghost"), toastCol(),
	})
	ev2, err := p.Parse(rawUpdate, false)
	require.NoError(t, err)
	require.NotNil(t, ev2)

	var after map[string]any
	require.NoError(t, json.Unmarshal(ev2.After, &after))
	// content was TOAST and cache was evicted — should be absent or nil
	_, hasContent := after["content"]
	assert.False(t, hasContent, "evicted TOAST column must be absent from after row")
}

// TestSchemaChangeReplacesCache verifies that a second RelationMessage for the same
// RelationID replaces the cached entry (schema evolution).
func TestSchemaChangeReplacesCache(t *testing.T) {
	p := newParser()

	// First schema: two columns.
	cols1 := []pglogrepl.RelationMessageColumn{
		{Flags: 1, Name: "id", DataType: 23, TypeModifier: -1},
		{Flags: 0, Name: "old_col", DataType: 25, TypeModifier: -1},
	}
	sendRelation(t, p, testRelID, testNamespace, testRelName, cols1)

	// Second schema: different columns.
	cols2 := []pglogrepl.RelationMessageColumn{
		{Flags: 1, Name: "id", DataType: 23, TypeModifier: -1},
		{Flags: 0, Name: "new_col", DataType: 25, TypeModifier: -1},
	}
	sendRelation(t, p, testRelID, testNamespace, testRelName, cols2)

	// Insert with new schema.
	rawInsert := encodeInsert(testRelID, []tupleCol{textCol("5"), textCol("new_value")})
	ev, err := p.Parse(rawInsert, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	var after map[string]any
	require.NoError(t, json.Unmarshal(ev.After, &after))
	// new_col present, old_col absent.
	assert.Equal(t, "new_value", after["new_col"])
	_, hasOld := after["old_col"]
	assert.False(t, hasOld, "old column must be absent after schema change")
}

// TestUnknownRelationIDReturnsError verifies that an Insert for an unseen RelationID
// returns an error rather than panicking.
func TestUnknownRelationIDReturnsError(t *testing.T) {
	p := newParser()
	// Do NOT send Relation message — send Insert directly.
	raw := encodeInsert(99999, []tupleCol{textCol("1")})
	ev, err := p.Parse(raw, false)
	assert.Error(t, err)
	assert.Nil(t, ev)
	assert.Contains(t, err.Error(), "99999")
}

// TestBeginAndCommitReturnNil verifies that Begin and Commit messages return nil
// events without error.
func TestBeginAndCommitReturnNil(t *testing.T) {
	p := newParser()

	rawBegin := encodeBegin(pglogrepl.LSN(0x1A2B3C4))
	ev, err := p.Parse(rawBegin, false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	rawCommit := encodeCommit(pglogrepl.LSN(0x1A2B3C4))
	ev, err = p.Parse(rawCommit, false)
	require.NoError(t, err)
	assert.Nil(t, ev)
}

// TestIdempotencyKeyFormat verifies the idempotency key matches the required format.
func TestIdempotencyKeyFormat(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	// Set the current LSN via Begin message.
	lsn := pglogrepl.LSN(0x1A2B3C4)
	sendBegin(t, p, lsn)

	raw := encodeInsert(testRelID, []tupleCol{textCol("42"), textCol("Item"), textCol("body")})
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	// Expected format: "sourceID:schema.table:pkStr:op:lsn"
	// pkStr is JSON-marshaled primary key map, e.g. {"id":"42"}
	// NOTE: pkStr may contain colons (JSON syntax), so we validate by known prefix/suffix anchors.
	key := ev.IdempotencyKey
	prefix := fmt.Sprintf("main-pg:%s.%s:", testNamespace, testRelName)
	suffix := fmt.Sprintf(":insert:%s", lsn.String())
	require.True(t, strings.HasPrefix(key, prefix), "idempotency key must start with %q, got: %q", prefix, key)
	require.True(t, strings.HasSuffix(key, suffix), "idempotency key must end with %q, got: %q", suffix, key)

	// Extract pkStr: portion between the table segment and the op:lsn suffix.
	pkStr := key[len(prefix) : len(key)-len(suffix)]
	var pkMap map[string]any
	require.NoError(t, json.Unmarshal([]byte(pkStr), &pkMap), "pkStr must be valid JSON, got: %q", pkStr)
	assert.Equal(t, "42", pkMap["id"])
}

// encodeUpdateWithOldTuple builds an Update message with both old and new tuples.
// Wire format: 'U' | uint32(RelationID) | 'O' | TupleData(oldCols) | 'N' | TupleData(newCols)
// Uses 'O' marker (REPLICA IDENTITY FULL) so pglogrepl populates m.OldTuple.
func encodeUpdateWithOldTuple(relID uint32, oldCols, newCols []tupleCol) []byte {
	var buf []byte
	buf = append(buf, 'U')
	buf = appendUint32(buf, relID)
	buf = append(buf, 'O')         // old tuple marker: REPLICA IDENTITY FULL
	buf = encodeTuple(buf, oldCols)
	buf = append(buf, 'N')         // new tuple marker
	buf = encodeTuple(buf, newCols)
	return buf
}

// encodeDeleteWithOldTuple builds a Delete message with a full old tuple.
// Wire format: 'D' | uint32(RelationID) | 'O' | TupleData(oldCols)
func encodeDeleteWithOldTuple(relID uint32, oldCols []tupleCol) []byte {
	var buf []byte
	buf = append(buf, 'D')
	buf = appendUint32(buf, relID)
	buf = append(buf, 'O')         // old tuple marker: REPLICA IDENTITY FULL
	buf = encodeTuple(buf, oldCols)
	return buf
}

// TestUpdateOldTuple_PopulatesBefore verifies that handleUpdate populates
// ChangeEvent.Before when m.OldTuple is present (EVT-01, PAR-01).
func TestUpdateOldTuple_PopulatesBefore(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	sendBegin(t, p, pglogrepl.LSN(0x100))

	oldCols := []tupleCol{textCol("42"), textCol("OldName"), textCol("old content")}
	newCols := []tupleCol{textCol("42"), textCol("NewName"), textCol("new content")}
	raw := encodeUpdateWithOldTuple(testRelID, oldCols, newCols)

	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	// Before must be non-nil and contain the old row data.
	require.NotNil(t, ev.Before, "Before must be populated for UPDATE with OldTuple (EVT-01)")
	var before map[string]any
	require.NoError(t, json.Unmarshal(ev.Before, &before))
	assert.Equal(t, "OldName", before["name"], "Before.name must be the old value")

	// After must still be the new row data.
	require.NotNil(t, ev.After)
	var after map[string]any
	require.NoError(t, json.Unmarshal(ev.After, &after))
	assert.Equal(t, "NewName", after["name"], "After.name must be the new value")
}

// TestDeleteOldTuple_PopulatesBefore verifies that handleDelete populates
// ChangeEvent.Before when m.OldTuple is present (EVT-01, PAR-01).
func TestDeleteOldTuple_PopulatesBefore(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	sendBegin(t, p, pglogrepl.LSN(0x100))

	oldCols := []tupleCol{textCol("99"), textCol("DeletedRow"), textCol("some content")}
	raw := encodeDeleteWithOldTuple(testRelID, oldCols)

	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	// Before must be non-nil and contain the deleted row data.
	require.NotNil(t, ev.Before, "Before must be populated for DELETE with OldTuple (EVT-01)")
	var before map[string]any
	require.NoError(t, json.Unmarshal(ev.Before, &before))
	assert.Equal(t, "DeletedRow", before["name"], "Before.name must be the deleted row value")

	// After must be nil for a DELETE.
	assert.Nil(t, ev.After, "After must be nil for DELETE")
}

// TestNullColumnInInsert verifies that null columns ('n') produce nil values in the after-row.
func TestNullColumnInInsert(t *testing.T) {
	p := newParser()
	cols := []pglogrepl.RelationMessageColumn{
		{Flags: 1, Name: "id", DataType: 23, TypeModifier: -1},
		{Flags: 0, Name: "description", DataType: 25, TypeModifier: -1},
	}
	sendRelation(t, p, testRelID, testNamespace, "products", cols)

	raw := encodeInsert(testRelID, []tupleCol{textCol("7"), nullCol()})
	ev, err := p.Parse(raw, false)
	require.NoError(t, err)
	require.NotNil(t, ev)

	var after map[string]any
	require.NoError(t, json.Unmarshal(ev.After, &after))
	assert.Equal(t, "7", after["id"])
	assert.Nil(t, after["description"])
}
