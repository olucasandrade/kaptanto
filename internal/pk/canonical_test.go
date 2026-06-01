package pk

import (
	"encoding/json"
	"testing"
)

// TestCanonicalByteIdentical verifies that WAL-style (string values) and
// snapshot-style (native Go types from pgx) maps for the same logical row
// produce byte-identical JSON after Canonical().
func TestCanonicalByteIdentical(t *testing.T) {
	cases := []struct {
		name     string
		walMap   map[string]any // as extractPK builds it (all strings)
		snapMap  map[string]any // as pgx rows.Values() returns it
	}{
		{
			name:    "int32 pk",
			walMap:  map[string]any{"id": "42"},
			snapMap: map[string]any{"id": int32(42)},
		},
		{
			name:    "int64 pk",
			walMap:  map[string]any{"id": "9007199254740993"},
			snapMap: map[string]any{"id": int64(9007199254740993)},
		},
		{
			name:    "int16 pk",
			walMap:  map[string]any{"id": "1000"},
			snapMap: map[string]any{"id": int16(1000)},
		},
		{
			name:    "string pk",
			walMap:  map[string]any{"code": "ABC"},
			snapMap: map[string]any{"code": "ABC"},
		},
		{
			name:    "bool pk",
			walMap:  map[string]any{"flag": "true"},
			snapMap: map[string]any{"flag": true},
		},
		{
			name:    "null pk",
			walMap:  map[string]any{"id": nil},
			snapMap: map[string]any{"id": nil},
		},
		{
			name:    "composite pk int+string",
			walMap:  map[string]any{"tenant_id": "7", "order_id": "abc"},
			snapMap: map[string]any{"tenant_id": int32(7), "order_id": "abc"},
		},
		{
			name:    "uint32 pk",
			walMap:  map[string]any{"id": "100"},
			snapMap: map[string]any{"id": uint32(100)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			walBytes, err := Canonical(tc.walMap)
			if err != nil {
				t.Fatalf("Canonical(walMap): %v", err)
			}
			snapBytes, err := Canonical(tc.snapMap)
			if err != nil {
				t.Fatalf("Canonical(snapMap): %v", err)
			}
			if string(walBytes) != string(snapBytes) {
				t.Errorf("byte mismatch:\n  WAL:  %s\n  snap: %s", walBytes, snapBytes)
			}
		})
	}
}

// TestCanonicalPartitionConsistency verifies that after Canonical(), the
// WAL and snapshot representations hash to the same partition (simulated by
// comparing the raw JSON bytes, which are the input to FNV-1a partitioning).
func TestCanonicalPartitionConsistency(t *testing.T) {
	// These are the two forms of the same integer PK that used to diverge.
	walMap := map[string]any{"id": "42"}
	snapMap := map[string]any{"id": int32(42)}

	walBytes, _ := Canonical(walMap)
	snapBytes, _ := Canonical(snapMap)

	if string(walBytes) != string(snapBytes) {
		t.Fatalf("partition key mismatch: WAL=%s snap=%s", walBytes, snapBytes)
	}
}

// TestCanonicalIntRoundtrip checks that the string form of an int is valid JSON
// when re-parsed (i.e., the resulting JSON {"id":"42"} is parseable).
func TestCanonicalIntRoundtrip(t *testing.T) {
	m := map[string]any{"id": int32(123)}
	b, err := Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v — raw: %s", err, b)
	}
	if out["id"] != "123" {
		t.Errorf("expected \"123\", got %q", out["id"])
	}
}
