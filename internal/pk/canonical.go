// Package pk provides helpers for normalizing primary-key values to a canonical
// string representation that is byte-identical between the WAL path and the
// snapshot path, satisfying the BKF-02 watermark deduplication invariant.
//
// The WAL path (internal/parser/pgoutput) decodes every PK column from the
// pgoutput *text* format, so integer 42 arrives as the Go string "42".
// The snapshot path (internal/backfill) receives native Go types from pgx
// (int32/int64/uint32/…), so the same integer would marshal as the JSON number
// 42 — a different byte sequence.
//
// Canonical() converts every value in a PK map to its string representation
// before JSON marshalling so both paths produce {"id":"42"}.
package pk

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Canonical converts each value in pkMap to the string form that the WAL path
// (extractPK in internal/parser/pgoutput/types.go) produces, then returns the
// resulting JSON bytes.  The canonical rule is: every non-nil value is
// represented as a string — matching Postgres text format.
//
// Supported value types (covers all common PK column types):
//
//   - nil                 → JSON null
//   - string              → kept as-is
//   - int, int8..int64    → decimal string
//   - uint, uint8..uint64 → decimal string
//   - float32, float64    → strconv.FormatFloat (shortest representation)
//   - bool                → "true" / "false"
//   - []byte              → hex string (same as Postgres bytea text format)
//   - fmt.Stringer        → .String()
//   - everything else     → fmt.Sprintf("%v", v)
//
// Map key ordering is determined by json.Marshal (sorted), which is the same
// for both paths, so composite PKs remain deterministic.
func Canonical(pkMap map[string]any) (json.RawMessage, error) {
	out := make(map[string]any, len(pkMap))
	for k, v := range pkMap {
		out[k] = canonicalValue(v)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("pk.Canonical: marshal: %w", err)
	}
	return b, nil
}

// canonicalValue converts a single PK column value to its canonical string
// representation, or nil for NULL.
func canonicalValue(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.FormatInt(int64(t), 10)
	case int8:
		return strconv.FormatInt(int64(t), 10)
	case int16:
		return strconv.FormatInt(int64(t), 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint8:
		return strconv.FormatUint(uint64(t), 10)
	case uint16:
		return strconv.FormatUint(uint64(t), 10)
	case uint32:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []byte:
		return fmt.Sprintf("%x", t)
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
