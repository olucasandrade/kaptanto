// Package pgoutput_test structural equality tests for the FFI hot path.
//
// This file has NO build tag — it runs under both:
//   - CGO_ENABLED=0 go test ./internal/parser/pgoutput/... (pure-Go path, selects ffi_stub.go)
//   - CGO_ENABLED=1 go test --tags rust ./internal/parser/pgoutput/... (Rust path, selects ffi_rust.go)
//
// Both paths must pass. Each path is verified independently — you cannot link
// ffi_stub.go and ffi_rust.go in the same binary, so cross-comparison in a single
// test binary is not possible.
//
// Structural equality strategy (CRITICAL):
// Raw byte equality between Go and Rust paths is intentionally NOT tested here.
// Go's encoding/json serializes map[string]any in non-deterministic key order,
// so bytes.Equal(goOutput, rustOutput) would be a flaky test. Instead, each path
// produces JSON that is parsed with encoding/json and compared field-by-field.
// Both paths must produce correct field values — that is the structural equality criterion.
package pgoutput_test

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/parser/pgoutput"
)

// assertJSONField parses jsonBytes into a map and checks that key has the expected value.
// This is the structural equality approach: parse then compare, never bytes.Equal.
func assertJSONField(t *testing.T, label string, jsonBytes []byte, key string, want any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		t.Fatalf("%s: unmarshal JSON: %v (bytes: %q)", label, err, jsonBytes)
	}
	got, ok := m[key]
	if !ok {
		t.Fatalf("%s: key %q missing from JSON %q", label, key, jsonBytes)
	}
	// Compare via JSON round-trip to normalize numeric types (e.g. float64 vs int).
	wantJ, _ := json.Marshal(want)
	gotJ, _ := json.Marshal(got)
	if string(wantJ) != string(gotJ) {
		t.Errorf("%s: key %q: got %q, want %q", label, key, gotJ, wantJ)
	}
}

// ffiTestParser creates a fresh Parser for FFI tests.
func ffiTestParser() *pgoutput.Parser {
	return pgoutput.New("ffi-test", event.NewIDGenerator())
}

// ffiTestRelCols returns the standard test relation columns shared with parser_test.go fixtures.
var ffiTestRelCols = []pglogrepl.RelationMessageColumn{
	{Flags: 1, Name: "id", DataType: 23, TypeModifier: -1},
	{Flags: 0, Name: "email", DataType: 25, TypeModifier: -1},
	{Flags: 0, Name: "bio", DataType: 25, TypeModifier: -1}, // TOAST-able column
}

// TestParserFFI_StructuralEquality_Note documents the test strategy for readers.
// Raw byte comparison is not applicable: Go map JSON key order is non-deterministic.
// Both pure-Go and Rust paths are verified independently in this file.
// Each must produce correct field values — that is the structural equality criterion.
func TestParserFFI_StructuralEquality_Note(t *testing.T) {
	t.Log("Structural equality: both pure-Go and Rust paths verified independently.")
	t.Log("Raw byte comparison is not applicable: Go map JSON key order is non-deterministic.")
	t.Log("Each path verified by parsing JSON output and comparing field values.")
}

// TestParserFFI_Insert verifies that an INSERT WAL message produces a ChangeEvent
// with correct Operation, Table, Key, and After fields.
// Runs under BOTH !rust and rust build tags.
func TestParserFFI_Insert(t *testing.T) {
	p := ffiTestParser()

	// Register relation schema (reuses wire encoding helpers from parser_test.go).
	relRaw := encodeRelation(testRelID, testNamespace, "users", ffiTestRelCols)
	_, err := p.Parse(relRaw, false)
	if err != nil {
		t.Fatalf("Parse Relation: %v", err)
	}

	// INSERT row: id=1, email=test@example.com, bio=hello
	insertRaw := encodeInsert(testRelID, []tupleCol{
		textCol("1"),
		textCol("test@example.com"),
		textCol("hello"),
	})
	ev, err := p.Parse(insertRaw, false)
	if err != nil {
		t.Fatalf("Parse Insert: %v", err)
	}
	if ev == nil {
		t.Fatal("Parse returned nil event for INSERT")
	}

	if ev.Operation != event.OpInsert {
		t.Errorf("Operation: got %q, want %q", ev.Operation, event.OpInsert)
	}
	if ev.Table != "users" {
		t.Errorf("Table: got %q, want %q", ev.Table, "users")
	}
	if ev.Schema != testNamespace {
		t.Errorf("Schema: got %q, want %q", ev.Schema, testNamespace)
	}

	// After must contain all three columns.
	assertJSONField(t, "After", ev.After, "id", "1")
	assertJSONField(t, "After", ev.After, "email", "test@example.com")
	assertJSONField(t, "After", ev.After, "bio", "hello")

	// Key must contain the PK column.
	assertJSONField(t, "Key", ev.Key, "id", "1")

	// Before must be nil for INSERT.
	if ev.Before != nil {
		t.Errorf("Before: expected nil for INSERT, got %q", ev.Before)
	}
}

// TestParserFFI_Update_TOAST verifies that an UPDATE WAL message where one column
// has data_type 'u' (TOAST, unchanged) produces After JSON with the TOAST column
// merged from the cached prevRow.
func TestParserFFI_Update_TOAST(t *testing.T) {
	p := ffiTestParser()

	relRaw := encodeRelation(testRelID, testNamespace, "users", ffiTestRelCols)
	_, err := p.Parse(relRaw, false)
	if err != nil {
		t.Fatalf("Parse Relation: %v", err)
	}

	// Insert to populate TOAST cache: id=42, email=alice@example.com, bio=<large text>
	insertRaw := encodeInsert(testRelID, []tupleCol{
		textCol("42"),
		textCol("alice@example.com"),
		textCol("large cached biography"),
	})
	_, err = p.Parse(insertRaw, false)
	if err != nil {
		t.Fatalf("Parse Insert (cache seeding): %v", err)
	}

	// Update: id=42, email changed, bio is TOAST (unchanged large column).
	updateRaw := encodeUpdate(testRelID, []tupleCol{
		textCol("42"),
		textCol("alice-updated@example.com"),
		toastCol(), // 'u' — unchanged large column
	})
	ev, err := p.Parse(updateRaw, false)
	if err != nil {
		t.Fatalf("Parse Update: %v", err)
	}
	if ev == nil {
		t.Fatal("Parse returned nil event for UPDATE")
	}

	if ev.Operation != event.OpUpdate {
		t.Errorf("Operation: got %q, want %q", ev.Operation, event.OpUpdate)
	}

	// After must contain merged TOAST value.
	assertJSONField(t, "After", ev.After, "id", "42")
	assertJSONField(t, "After", ev.After, "email", "alice-updated@example.com")
	// bio must be the cached value from the prior INSERT — TOAST merge.
	assertJSONField(t, "After", ev.After, "bio", "large cached biography")
}

// TestParserFFI_Delete verifies that a DELETE WAL message produces a ChangeEvent
// with Before JSON containing PK columns and After being nil.
func TestParserFFI_Delete(t *testing.T) {
	p := ffiTestParser()

	relRaw := encodeRelation(testRelID, testNamespace, "users", ffiTestRelCols)
	_, err := p.Parse(relRaw, false)
	if err != nil {
		t.Fatalf("Parse Relation: %v", err)
	}

	// DELETE with key tuple only (REPLICA IDENTITY DEFAULT — key columns only).
	deleteRaw := encodeDelete(testRelID, []tupleCol{
		textCol("7"), // PK column id=7
		nullCol(),
		nullCol(),
	})
	ev, err := p.Parse(deleteRaw, false)
	if err != nil {
		t.Fatalf("Parse Delete: %v", err)
	}
	if ev == nil {
		t.Fatal("Parse returned nil event for DELETE")
	}

	if ev.Operation != event.OpDelete {
		t.Errorf("Operation: got %q, want %q", ev.Operation, event.OpDelete)
	}

	// After must be nil for DELETE.
	if ev.After != nil {
		t.Errorf("After: expected nil for DELETE, got %q", ev.After)
	}

	// Key must contain the PK column.
	assertJSONField(t, "Key", ev.Key, "id", "7")
}

// TestParserFFI_MalformedWAL verifies that calling Parser.Parse with malformed
// WAL data returns (nil, error) and does not panic.
func TestParserFFI_MalformedWAL(t *testing.T) {
	p := ffiTestParser()

	// Pass random bytes that cannot be a valid pgoutput message.
	malformed := []byte{0xFF, 0xFE, 0x00, 0x01, 0x02, 0x03}
	ev, err := p.Parse(malformed, false)
	if err == nil {
		t.Error("expected error for malformed WAL data, got nil")
	}
	if ev != nil {
		t.Errorf("expected nil event for malformed WAL data, got %+v", ev)
	}
}
