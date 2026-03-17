//go:build rust

package pgoutput

/*
#cgo linux LDFLAGS: -L${SRCDIR}/../../../rust/kaptanto-ffi/target/release -lkaptanto_ffi -ldl -lpthread
#cgo darwin LDFLAGS: -L${SRCDIR}/../../../rust/kaptanto-ffi/target/release -lkaptanto_ffi -framework CoreFoundation -framework Security
#include "../../../rust/kaptanto-ffi/include/kaptanto_ffi.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"unsafe"

	"github.com/jackc/pglogrepl"
)

// toastHandle is an opaque pointer to the Rust-owned TOAST cache.
type toastHandle = unsafe.Pointer

// encodeColumns serializes a pglogrepl column slice into the length-prefixed
// binary wire format expected by kaptanto_decode_serialize.
//
// Wire format:
//
//	[4 bytes big-endian uint32: column count]
//	For each column:
//	  [1 byte: data_type ('n','u','t','b')]
//	  [4 bytes big-endian uint32: data_len]
//	  [data_len bytes: data]
func encodeColumns(cols []*pglogrepl.TupleDataColumn) []byte {
	// Pre-calculate size to allocate once.
	size := 4
	for _, col := range cols {
		size += 1 + 4 + len(col.Data)
	}
	buf := make([]byte, 0, size)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(len(cols)))
	buf = append(buf, tmp[:]...)
	for _, col := range cols {
		buf = append(buf, col.DataType)
		binary.BigEndian.PutUint32(tmp[:], uint32(len(col.Data)))
		buf = append(buf, tmp[:]...)
		buf = append(buf, col.Data...)
	}
	return buf
}

// encodeSchema serializes relation column names as a JSON array.
func encodeSchema(rel *pglogrepl.RelationMessageV2) []byte {
	// Build ["col1","col2",...] manually to avoid encoding/json import.
	out := make([]byte, 0, 2+len(rel.Columns)*16)
	out = append(out, '[')
	for i, c := range rel.Columns {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, []byte(c.Name)...)
		out = append(out, '"')
	}
	out = append(out, ']')
	return out
}

// decodeAndSerializeRow encodes the column tuple as length-prefixed binary,
// calls kaptanto_decode_serialize in Rust, and returns the resulting JSON bytes.
// One CGO call per DML operation — not per column.
func decodeAndSerializeRow(
	rel *pglogrepl.RelationMessageV2,
	cols []*pglogrepl.TupleDataColumn,
	prevRow map[string]any, // unused in Rust path; TOAST resolved via Rust cache
) ([]byte, error) {
	colBytes := encodeColumns(cols)
	schemaBytes := encodeSchema(rel)

	// Copy to C-managed memory (CGO pointer rule: Go memory must not contain Go pointers).
	cColData := C.CBytes(colBytes)
	defer C.free(cColData)
	cSchema := C.CBytes(schemaBytes)
	defer C.free(cSchema)

	var outLen C.size_t
	ptr := C.kaptanto_decode_serialize(
		(*C.uchar)(cColData),
		C.size_t(len(colBytes)),
		(*C.uchar)(cSchema),
		C.size_t(len(schemaBytes)),
		&outLen,
	)
	if ptr == nil {
		return nil, errors.New("rust: kaptanto_decode_serialize returned nil")
	}
	result := C.GoBytes(unsafe.Pointer(ptr), C.int(outLen))
	C.kaptanto_free_buf(ptr, outLen)
	return result, nil
}
