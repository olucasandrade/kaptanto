package output_test

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/output"
)

// TestApplyColumnFilter_NilInput verifies that a nil raw JSON input passes through unchanged.
func TestApplyColumnFilter_NilInput(t *testing.T) {
	result, err := output.ApplyColumnFilter(nil, []string{"id"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for nil input, got %v", result)
	}
}

// TestApplyColumnFilter_NilAllowList verifies that a nil allow-list is a pass-through.
func TestApplyColumnFilter_NilAllowList(t *testing.T) {
	raw := json.RawMessage(`{"id":1,"status":"ok"}`)
	result, err := output.ApplyColumnFilter(raw, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(raw) {
		t.Errorf("expected pass-through for nil allow-list, got %q", result)
	}
}

// TestApplyColumnFilter_EmptyAllowList verifies that an empty allow-list is a pass-through.
func TestApplyColumnFilter_EmptyAllowList(t *testing.T) {
	raw := json.RawMessage(`{"id":1,"status":"ok"}`)
	result, err := output.ApplyColumnFilter(raw, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(raw) {
		t.Errorf("expected pass-through for empty allow-list, got %q", result)
	}
}

// TestApplyColumnFilter_FiltersColumns verifies that only allowed columns appear in the output.
func TestApplyColumnFilter_FiltersColumns(t *testing.T) {
	raw := json.RawMessage(`{"id":1,"status":"ok","internal":"x"}`)
	result, err := output.ApplyColumnFilter(raw, []string{"id", "status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, ok := got["internal"]; ok {
		t.Error("'internal' key should have been stripped")
	}
	if got["id"] == nil {
		t.Error("'id' should be present")
	}
	if got["status"] == nil {
		t.Error("'status' should be present")
	}
}

// TestApplyColumnFilter_NoMutation verifies the result is a NEW json.RawMessage (input not mutated).
func TestApplyColumnFilter_NoMutation(t *testing.T) {
	rawJSON := `{"id":1,"status":"ok","internal":"x"}`
	raw := json.RawMessage(rawJSON)
	original := make(json.RawMessage, len(raw))
	copy(original, raw)

	result, err := output.ApplyColumnFilter(raw, []string{"id", "status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Input must not be mutated.
	if string(raw) != string(original) {
		t.Errorf("input was mutated: got %q, want %q", raw, original)
	}

	// Result must be a different slice (not sharing backing array).
	if &result[0] == &raw[0] {
		t.Error("result shares backing array with input — shared-slice bug")
	}
}

// TestApplyColumnFilter_NonObjectJSON verifies that non-object JSON passes through unchanged.
func TestApplyColumnFilter_NonObjectJSON(t *testing.T) {
	cases := []string{`[1,2,3]`, `42`, `"hello"`, `true`, `null`}
	allowed := []string{"id"}
	for _, tc := range cases {
		raw := json.RawMessage(tc)
		result, err := output.ApplyColumnFilter(raw, allowed)
		if err != nil {
			t.Errorf("input %q: unexpected error: %v", tc, err)
		}
		if string(result) != tc {
			t.Errorf("input %q: expected pass-through, got %q", tc, result)
		}
	}
}

// TestBuildAllowSet verifies set construction and the nil-for-empty contract.
func TestBuildAllowSet(t *testing.T) {
	if set := output.BuildAllowSet(nil); set != nil {
		t.Errorf("nil list should produce nil set, got %v", set)
	}
	if set := output.BuildAllowSet([]string{}); set != nil {
		t.Errorf("empty list should produce nil set, got %v", set)
	}
	set := output.BuildAllowSet([]string{"id", "status"})
	if len(set) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(set))
	}
	if _, ok := set["id"]; !ok {
		t.Error("'id' missing from set")
	}
	if _, ok := set["status"]; !ok {
		t.Error("'status' missing from set")
	}
}

// TestApplyColumnFilterSet_MatchesApplyColumnFilter verifies the precomputed-set
// variant produces byte-identical results to the convenience wrapper across the
// same inputs Deliver sees: nil/empty sets, object filtering, and non-objects.
func TestApplyColumnFilterSet_MatchesApplyColumnFilter(t *testing.T) {
	inputs := []json.RawMessage{
		nil,
		json.RawMessage(`{"id":1,"status":"ok","internal":"x"}`),
		json.RawMessage(`[1,2,3]`),
		json.RawMessage(`null`),
		json.RawMessage(`"hello"`),
	}
	allowLists := [][]string{nil, {}, {"id"}, {"id", "status"}}

	for _, allowed := range allowLists {
		set := output.BuildAllowSet(allowed)
		for _, raw := range inputs {
			want, werr := output.ApplyColumnFilter(raw, allowed)
			got, gerr := output.ApplyColumnFilterSet(raw, set)
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("allowed=%v raw=%s: err mismatch: want %v got %v", allowed, raw, werr, gerr)
			}
			if string(want) != string(got) {
				t.Errorf("allowed=%v raw=%s: want %q got %q", allowed, raw, want, got)
			}
		}
	}
}
