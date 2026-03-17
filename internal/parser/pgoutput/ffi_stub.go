//go:build !rust

package pgoutput

import (
	"encoding/json"

	"github.com/jackc/pglogrepl"
)

// decodeAndSerializeRow decodes the column tuple and serializes the row to JSON.
// Pure-Go path: uses decodeColumns + encoding/json.
func decodeAndSerializeRow(
	rel *pglogrepl.RelationMessageV2,
	cols []*pglogrepl.TupleDataColumn,
	prevRow map[string]any,
) ([]byte, error) {
	row := decodeColumns(rel, cols, prevRow)
	return json.Marshal(row)
}

// toastHandle is the pure-Go TOAST cache reference (the *TOASTCache itself).
// Under the !rust tag, Parser.toastHandle is unused — Parser.toast is used directly.
// This type alias lets parser.go compile under both tags without ifdefs.
type toastHandle = *TOASTCache
