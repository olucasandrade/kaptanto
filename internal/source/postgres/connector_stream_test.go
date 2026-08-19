package postgres

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

type streamEventLog struct {
	events []*event.ChangeEvent
}

func (m *streamEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	m.events = append(m.events, ev)
	return uint64(len(m.events)), nil
}

func (m *streamEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	seqs := make([]uint64, len(evs))
	for i, ev := range evs {
		m.events = append(m.events, ev)
		seqs[i] = uint64(len(m.events))
	}
	return seqs, nil
}

func (m *streamEventLog) ReadPartition(_ context.Context, _ uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	return nil, nil
}

func (m *streamEventLog) Close() error { return nil }

type streamCheckpointStore struct {
	saves []string
}

func (m *streamCheckpointStore) Save(_ context.Context, _, lsn string) error {
	m.saves = append(m.saves, lsn)
	return nil
}

func (m *streamCheckpointStore) Load(_ context.Context, _ string) (string, error) { return "", nil }
func (m *streamCheckpointStore) Close() error                                     { return nil }

var (
	_ eventlog.EventLog          = (*streamEventLog)(nil)
	_ checkpoint.CheckpointStore = (*streamCheckpointStore)(nil)
)

const (
	streamTestRelID = uint32(12345)
	streamTestXID   = uint32(42)
)

func streamPutU32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return append(buf, b...)
}

func streamPutU64(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return append(buf, b...)
}

func streamPutStr(buf []byte, s string) []byte {
	return append(append(buf, s...), 0)
}

func binaryAppendU16(buf []byte, v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return append(buf, b...)
}

func encodeStreamRelation() []byte {
	buf := []byte{'R'}
	buf = streamPutU32(buf, streamTestRelID)
	buf = streamPutStr(buf, "public")
	buf = streamPutStr(buf, "orders")
	buf = append(buf, 'd')
	buf = binaryAppendU16(buf, 3)
	cols := []struct {
		flags byte
		name  string
		oid   uint32
	}{
		{1, "id", 23},
		{0, "name", 25},
		{0, "content", 25},
	}
	for _, col := range cols {
		buf = append(buf, col.flags)
		buf = streamPutStr(buf, col.name)
		buf = streamPutU32(buf, col.oid)
		buf = streamPutU32(buf, uint32(0xFFFFFFFF))
	}
	return buf
}

func encodeStreamTuple(buf []byte, cols []string) []byte {
	buf = binaryAppendU16(buf, uint16(len(cols)))
	for _, s := range cols {
		buf = append(buf, 't')
		buf = streamPutU32(buf, uint32(len(s)))
		buf = append(buf, s...)
	}
	return buf
}

func encodeStreamInsert(pk string) []byte {
	buf := []byte{'I'}
	buf = streamPutU32(buf, streamTestRelID)
	buf = append(buf, 'N')
	return encodeStreamTuple(buf, []string{pk, "n", "b"})
}

func encodeInStreamInsert(pk string) []byte {
	inner := encodeStreamInsert(pk)
	out := []byte{'I'}
	out = streamPutU32(out, streamTestXID)
	return append(out, inner[1:]...)
}

func encodeTestStreamStart(first uint8) []byte {
	buf := []byte{'S'}
	buf = streamPutU32(buf, streamTestXID)
	return append(buf, first)
}

func encodeTestStreamStop() []byte { return []byte{'E'} }

func encodeTestStreamCommit(lsn pglogrepl.LSN) []byte {
	buf := []byte{'c'}
	buf = streamPutU32(buf, streamTestXID)
	buf = append(buf, 0)
	buf = streamPutU64(buf, uint64(lsn))
	buf = streamPutU64(buf, uint64(lsn))
	buf = streamPutU64(buf, 0)
	return buf
}

func encodeTestStreamAbort() []byte {
	buf := []byte{'A'}
	buf = streamPutU32(buf, streamTestXID)
	return streamPutU32(buf, streamTestXID)
}

func encodeTestBegin(lsn pglogrepl.LSN) []byte {
	buf := []byte{'B'}
	buf = streamPutU64(buf, uint64(lsn))
	buf = streamPutU64(buf, 0)
	return streamPutU32(buf, 1)
}

func encodeTestCommit(lsn pglogrepl.LSN) []byte {
	buf := []byte{'C'}
	buf = append(buf, 0)
	buf = streamPutU64(buf, uint64(lsn))
	buf = streamPutU64(buf, uint64(lsn))
	return streamPutU64(buf, 0)
}

func newStreamConnector(t *testing.T) (*PostgresConnector, *streamEventLog, *streamCheckpointStore) {
	t.Helper()
	el := &streamEventLog{}
	store := &streamCheckpointStore{}
	c := NewWithEventLog(Config{DSN: "postgres://localhost/testdb", SourceID: "pg1"}, store, event.NewIDGenerator(), el)
	return c, el, store
}

func applyWAL(t *testing.T, c *PostgresConnector, st *walReceiveState, walStart pglogrepl.LSN, payload []byte) (pglogrepl.LSN, bool) {
	t.Helper()
	ack, committed, err := c.handleXLogData(context.Background(), st, pglogrepl.XLogData{
		WALStart: walStart,
		WALData:  payload,
	})
	if err != nil {
		t.Fatalf("handleXLogData: %v", err)
	}
	return ack, committed
}

func TestHandleXLogData_StreamCommitFlushesAndCheckpoints(t *testing.T) {
	c, el, store := newStreamConnector(t)
	st := &walReceiveState{}
	commitLSN := pglogrepl.LSN(0x1A2B3C4)
	pos := pglogrepl.LSN(0x100)

	_, committed := applyWAL(t, c, st, pos, encodeStreamRelation())
	if committed {
		t.Fatal("relation must not commit")
	}
	pos += 16
	_, committed = applyWAL(t, c, st, pos, encodeTestStreamStart(1))
	if committed || !st.streamedOpen {
		t.Fatalf("StreamStart: committed=%v streamedOpen=%v", committed, st.streamedOpen)
	}
	if len(el.events) != 0 {
		t.Fatalf("no rows before StreamCommit, got %d", len(el.events))
	}

	n := 3
	for i := 0; i < n; i++ {
		pos += 16
		applyWAL(t, c, st, pos, encodeInStreamInsert(fmt.Sprintf("%d", i+1)))
		if len(el.events) != 0 {
			t.Fatalf("insert %d flushed early", i)
		}
	}
	pos += 16
	applyWAL(t, c, st, pos, encodeTestStreamStop())
	pos += 16
	ack, committed := applyWAL(t, c, st, pos, encodeTestStreamCommit(commitLSN))
	if !committed {
		t.Fatal("StreamCommit must checkpoint")
	}
	if ack == 0 {
		t.Fatal("StreamCommit ack LSN must be non-zero")
	}
	if len(el.events) != n {
		t.Fatalf("got %d persisted rows, want %d", len(el.events), n)
	}
	if len(store.saves) != 1 {
		t.Fatalf("got %d checkpoint saves, want 1", len(store.saves))
	}
	lsnStr := commitLSN.String()
	for i, ev := range el.events {
		if got, _ := ev.Metadata["lsn"].(string); got != lsnStr {
			t.Errorf("event %d metadata lsn=%q want %q", i, got, lsnStr)
		}
		if !strings.Contains(ev.IdempotencyKey, ":"+lsnStr+":") {
			t.Errorf("event %d key %q missing commit LSN", i, ev.IdempotencyKey)
		}
		if !strings.HasSuffix(ev.IdempotencyKey, fmt.Sprintf(":%d", i)) {
			t.Errorf("event %d key %q want changeSeq %d", i, ev.IdempotencyKey, i)
		}
	}
}

func TestHandleXLogData_StreamAbortPersistsZeroRows(t *testing.T) {
	c, el, store := newStreamConnector(t)
	st := &walReceiveState{}
	pos := pglogrepl.LSN(0x200)
	applyWAL(t, c, st, pos, encodeStreamRelation())
	pos += 16
	applyWAL(t, c, st, pos, encodeTestStreamStart(1))
	for i := 0; i < 5; i++ {
		pos += 16
		applyWAL(t, c, st, pos, encodeInStreamInsert(fmt.Sprintf("%d", i+1)))
	}
	pos += 16
	applyWAL(t, c, st, pos, encodeTestStreamStop())
	pos += 16
	_, committed := applyWAL(t, c, st, pos, encodeTestStreamAbort())
	if !committed {
		t.Fatal("StreamAbort should still checkpoint so the slot can advance")
	}
	if len(el.events) != 0 {
		t.Fatalf("StreamAbort must persist zero rows, got %d", len(el.events))
	}
	if len(store.saves) != 1 {
		t.Fatalf("got %d saves, want 1", len(store.saves))
	}
	if st.streamedOpen {
		t.Fatal("streamedOpen must be false after abort")
	}
}

func TestHandleXLogData_NoMidFlushWhileStreamed(t *testing.T) {
	c, el, _ := newStreamConnector(t)
	st := &walReceiveState{}
	pos := pglogrepl.LSN(0x300)
	applyWAL(t, c, st, pos, encodeStreamRelation())
	pos += 16
	applyWAL(t, c, st, pos, encodeTestStreamStart(1))
	n := walBufFlushThreshold + 1
	for i := 0; i < n; i++ {
		pos += 16
		applyWAL(t, c, st, pos, encodeInStreamInsert(fmt.Sprintf("%d", i+1)))
		if len(el.events) != 0 {
			t.Fatalf("mid-flush at insert %d leaked %d rows", i, len(el.events))
		}
	}
	pos += 16
	applyWAL(t, c, st, pos, encodeTestStreamStop())
	pos += 16
	_, committed := applyWAL(t, c, st, pos, encodeTestStreamCommit(pglogrepl.LSN(0x999)))
	if !committed {
		t.Fatal("expected StreamCommit")
	}
	if len(el.events) != n {
		t.Fatalf("got %d rows after StreamCommit, want %d", len(el.events), n)
	}
}

func TestHandleXLogData_ReplayAfterClearRelationCacheReusesKeys(t *testing.T) {
	c, el, _ := newStreamConnector(t)
	drive := func(st *walReceiveState) {
		pos := pglogrepl.LSN(0x400)
		applyWAL(t, c, st, pos, encodeStreamRelation())
		pos += 16
		applyWAL(t, c, st, pos, encodeTestStreamStart(1))
		for i := 0; i < 3; i++ {
			pos += 16
			applyWAL(t, c, st, pos, encodeInStreamInsert(fmt.Sprintf("%d", i+1)))
		}
		pos += 16
		applyWAL(t, c, st, pos, encodeTestStreamStop())
		pos += 16
		_, committed := applyWAL(t, c, st, pos, encodeTestStreamCommit(pglogrepl.LSN(0xABC)))
		if !committed {
			t.Fatal("expected commit")
		}
	}

	drive(&walReceiveState{})
	first := make([]string, len(el.events))
	for i, ev := range el.events {
		first[i] = ev.IdempotencyKey
	}

	c.parser.ClearRelationCache()
	drive(&walReceiveState{})
	if len(el.events) != 6 {
		t.Fatalf("got %d events after replay, want 6", len(el.events))
	}
	for i := 0; i < 3; i++ {
		if el.events[i+3].IdempotencyKey != first[i] {
			t.Errorf("replay key[%d]=%q want %q", i, el.events[i+3].IdempotencyKey, first[i])
		}
	}
}

func TestHandleXLogData_RegularCommitStillFlushes(t *testing.T) {
	c, el, store := newStreamConnector(t)
	st := &walReceiveState{}
	lsn := pglogrepl.LSN(0x50)
	pos := pglogrepl.LSN(0x10)
	applyWAL(t, c, st, pos, encodeStreamRelation())
	pos += 16
	applyWAL(t, c, st, pos, encodeTestBegin(lsn))
	pos += 16
	applyWAL(t, c, st, pos, encodeStreamInsert("9"))
	if len(el.events) != 0 {
		t.Fatal("regular insert must wait for Commit")
	}
	pos += 16
	_, committed := applyWAL(t, c, st, pos, encodeTestCommit(lsn))
	if !committed {
		t.Fatal("Commit C must checkpoint")
	}
	if len(el.events) != 1 {
		t.Fatalf("got %d rows, want 1", len(el.events))
	}
	if len(store.saves) != 1 {
		t.Fatalf("got %d saves, want 1", len(store.saves))
	}
	if !strings.Contains(el.events[0].IdempotencyKey, ":"+lsn.String()+":") {
		t.Errorf("regular commit key %q should keep Begin LSN", el.events[0].IdempotencyKey)
	}
}

func TestRewriteIdempotencyLSN(t *testing.T) {
	ev := &event.ChangeEvent{IdempotencyKey: `pg1:public.orders:{"id":"1"}:insert:0/0:3`}
	rewriteIdempotencyLSN(ev, "0/1A2B3C4")
	want := `pg1:public.orders:{"id":"1"}:insert:0/1A2B3C4:3`
	if ev.IdempotencyKey != want {
		t.Fatalf("got %q want %q", ev.IdempotencyKey, want)
	}
}
