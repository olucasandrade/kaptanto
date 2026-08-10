//go:build !rust

package pgoutput

import (
	"encoding/json"

	"github.com/jackc/pglogrepl"
)

// decodeSerializeAndRow decodes a tuple to a row map and JSON bytes in one pass.
func decodeSerializeAndRow(
	rel *pglogrepl.RelationMessageV2,
	cols []*pglogrepl.TupleDataColumn,
	prevRow map[string]any,
) (map[string]any, []byte, error) {
	row := decodeColumns(rel, cols, prevRow)
	afterJSON, err := json.Marshal(row)
	if err != nil {
		return nil, nil, err
	}
	return row, afterJSON, nil
}

// toastHandle is the pure-Go TOAST cache reference (the *TOASTCache itself).
// Under the !rust tag, Parser.toastHandle is unused — Parser.toast is used directly.
// This type alias lets parser.go compile under both tags without ifdefs.
//nolint:unused // used under the `rust` build tag; unused only in the pure-Go build.
type toastHandle = *TOASTCache
