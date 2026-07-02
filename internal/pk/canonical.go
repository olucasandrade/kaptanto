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
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
//   - bool                → "t" / "f" (Postgres boolout format, as pgoutput emits)
//   - []byte              → \x<hex> (Postgres bytea hex format)
//   - [16]byte            → hyphenated UUID string (pgx returns [16]byte for uuid)
//   - time.Time           → Postgres ISO text ("2006-01-02 15:04:05.999999-07");
//     byte parity with the WAL path holds when the decoded offset matches the
//     server session TimeZone (pgx yields UTC for timestamptz, matching
//     servers running TimeZone=UTC — the common production setup)
//   - pgtype.Numeric      → decimal string via Value() (Postgres text form)
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
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		// Postgres boolout emits "t"/"f" (what pgoutput sends on the WAL path);
		// note this differs from the SQL cast true::text, which yields "true".
		if t {
			return "t"
		}
		return "f"
	case []byte:
		return fmt.Sprintf("\\x%x", t)
	case [16]byte:
		// pgx v5 rows.Values() decodes uuid columns to [16]byte
		// (pgtype.UUIDCodec.DecodeValue returns UUID.Bytes). The WAL path
		// receives the standard hyphenated lowercase text form.
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case time.Time:
		// Postgres ISO DateStyle text: fractional seconds without trailing
		// zeros, offset as ±HH (Go layout "-07"). Must precede the
		// fmt.Stringer case, which would produce Go's own time format.
		return t.Format("2006-01-02 15:04:05.999999-07")
	case pgtype.Numeric:
		if val, err := t.Value(); err == nil {
			if s, ok := val.(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", v)
	case *pgtype.Numeric:
		if val, err := t.Value(); err == nil {
			if s, ok := val.(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", v)
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
