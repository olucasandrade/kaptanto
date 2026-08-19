package pgoutput_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodeStreamStart(xid uint32, firstSegment uint8) []byte {
	buf := []byte{'S'}
	buf = appendUint32(buf, xid)
	return append(buf, firstSegment)
}

func encodeStreamStop() []byte { return []byte{'E'} }

func encodeStreamCommit(xid uint32, commitLSN pglogrepl.LSN) []byte {
	buf := []byte{'c'}
	buf = appendUint32(buf, xid)
	buf = append(buf, 0) // flags
	buf = appendUint64(buf, uint64(commitLSN))
	buf = appendUint64(buf, uint64(commitLSN)) // TransactionEndLSN
	buf = appendUint64(buf, 0)                 // CommitTime
	return buf
}

func encodeStreamAbort(xid, subxid uint32) []byte {
	buf := []byte{'A'}
	buf = appendUint32(buf, xid)
	return appendUint32(buf, subxid)
}

func encodeInStreamInsert(xid, relID uint32, cols []tupleCol) []byte {
	inner := encodeInsert(relID, cols)
	out := []byte{'I'}
	out = appendUint32(out, xid)
	return append(out, inner[1:]...)
}

func TestStreamStartResetsChangeSeqOnFirstSegment(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	sendBegin(t, p, pglogrepl.LSN(0x100))
	ev, err := p.Parse(encodeInsert(testRelID, []tupleCol{textCol("1"), textCol("a"), textCol("b")}), false)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.True(t, strings.HasSuffix(ev.IdempotencyKey, ":0"))

	ev, err = p.Parse(encodeStreamStart(42, 1), false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	ev, err = p.Parse(encodeInStreamInsert(42, testRelID, []tupleCol{textCol("2"), textCol("c"), textCol("d")}), false)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.True(t, strings.HasSuffix(ev.IdempotencyKey, ":0"),
		"first streamed change must reset changeSeq, got %q", ev.IdempotencyKey)
}

func TestStreamStartLaterSegmentKeepsChangeSeq(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)

	ev, err := p.Parse(encodeStreamStart(7, 1), false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	ev, err = p.Parse(encodeInStreamInsert(7, testRelID, []tupleCol{textCol("1"), textCol("a"), textCol("b")}), false)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.True(t, strings.HasSuffix(ev.IdempotencyKey, ":0"))

	ev, err = p.Parse(encodeStreamStop(), false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	ev, err = p.Parse(encodeStreamStart(7, 0), false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	ev, err = p.Parse(encodeInStreamInsert(7, testRelID, []tupleCol{textCol("2"), textCol("c"), textCol("d")}), false)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.True(t, strings.HasSuffix(ev.IdempotencyKey, ":1"),
		"continuation segment must not reset changeSeq, got %q", ev.IdempotencyKey)
}

func TestStreamCommitSetsCurrentLSN(t *testing.T) {
	p := newParser()
	commitLSN := pglogrepl.LSN(0x1A2B3C4)
	ev, err := p.Parse(encodeStreamCommit(9, commitLSN), false)
	require.NoError(t, err)
	assert.Nil(t, ev)
	assert.Equal(t, commitLSN, p.CurrentLSN())
}

func TestStreamAbortClearsInStream(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	_, err := p.Parse(encodeStreamStart(3, 1), false)
	require.NoError(t, err)

	ev, err := p.Parse(encodeStreamAbort(3, 3), false)
	require.NoError(t, err)
	assert.Nil(t, ev)

	// After abort, inserts must decode without the in-stream XID prefix.
	ev, err = p.Parse(encodeInsert(testRelID, []tupleCol{textCol("1"), textCol("a"), textCol("b")}), false)
	require.NoError(t, err)
	require.NotNil(t, ev)
}

func TestStreamReplayAfterClearRelationCacheReusesKeys(t *testing.T) {
	p := newParser()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	xid := uint32(11)
	commitLSN := pglogrepl.LSN(0xABC)

	drive := func() []string {
		_, err := p.Parse(encodeStreamStart(xid, 1), false)
		require.NoError(t, err)
		var keys []string
		for i, pk := range []string{"1", "2", "3"} {
			ev, err := p.Parse(encodeInStreamInsert(xid, testRelID, []tupleCol{textCol(pk), textCol("n"), textCol("b")}), false)
			require.NoError(t, err)
			require.NotNil(t, ev, "insert %d", i)
			keys = append(keys, ev.IdempotencyKey)
		}
		_, err = p.Parse(encodeStreamStop(), false)
		require.NoError(t, err)
		_, err = p.Parse(encodeStreamCommit(xid, commitLSN), false)
		require.NoError(t, err)
		return keys
	}

	first := drive()
	p.ClearRelationCache()
	sendRelation(t, p, testRelID, testNamespace, testRelName, testCols)
	second := drive()
	assert.Equal(t, first, second, "replay after ClearRelationCache must reuse idempotency keys")
}

func TestEncodeStreamStartWireFormat(t *testing.T) {
	raw := encodeStreamStart(0x01020304, 1)
	require.Equal(t, byte('S'), raw[0])
	require.Equal(t, uint32(0x01020304), binary.BigEndian.Uint32(raw[1:5]))
	require.Equal(t, byte(1), raw[5])
}
