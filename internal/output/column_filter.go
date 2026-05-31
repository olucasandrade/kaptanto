// Package output provides shared utilities for kaptanto output consumers.
// This file implements column-level filtering (CFG-05).
package output

import "encoding/json"

// ApplyColumnFilter strips keys from a JSON object that are not in the allowed list.
//
// Rules:
//   - If raw is nil, return nil, nil (JSON null passes through unchanged).
//   - If allowed is nil or empty, return raw, nil (pass-through — no column restriction).
//   - If raw is not a JSON object (e.g. array, number), return raw, nil (pass-through).
//   - Otherwise, return a new json.RawMessage containing only the allowed keys.
//
// The input slice is never mutated; the result is always a freshly allocated []byte.
//
// This is a convenience wrapper that builds the allow-set on every call. Hot
// paths that filter many events against a fixed allow-list should precompute the
// set once (see BuildAllowSet) and call ApplyColumnFilterSet directly.
func ApplyColumnFilter(raw json.RawMessage, allowed []string) (json.RawMessage, error) {
	// No allow-list = pass-through; avoid building an empty set.
	if len(allowed) == 0 {
		return raw, nil
	}
	return ApplyColumnFilterSet(raw, BuildAllowSet(allowed))
}

// BuildAllowSet converts a column allow-list into a set for O(1) membership
// checks. Returns nil for an empty/nil list so callers can treat nil as
// "no restriction" (pass-through). Compute this once at consumer construction
// rather than per event.
func BuildAllowSet(allowed []string) map[string]struct{} {
	if len(allowed) == 0 {
		return nil
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, col := range allowed {
		allowSet[col] = struct{}{}
	}
	return allowSet
}

// ApplyColumnFilterSet is ApplyColumnFilter against a precomputed allow-set.
// A nil/empty allowSet is a pass-through. The input slice is never mutated.
func ApplyColumnFilterSet(raw json.RawMessage, allowSet map[string]struct{}) (json.RawMessage, error) {
	// Nil raw = JSON null; pass through unchanged.
	if raw == nil {
		return nil, nil
	}

	// No allow-list = pass-through.
	if len(allowSet) == 0 {
		return raw, nil
	}

	// Unmarshal into a generic value first to detect non-object types.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// If the JSON is malformed, pass through and let the caller handle it.
		return raw, nil
	}

	obj, ok := v.(map[string]any)
	if !ok {
		// Non-object (array, number, string, bool, null) — pass through unchanged.
		return raw, nil
	}

	// Retain only allowed keys.
	filtered := make(map[string]any, len(allowSet))
	for k, val := range obj {
		if _, keep := allowSet[k]; keep {
			filtered[k] = val
		}
	}

	// Re-marshal into a fresh []byte — never aliases the input.
	out, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
