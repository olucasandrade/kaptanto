package postgres

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/event"
)

// encodeTestKeepalive builds a PrimaryKeepaliveMessage payload (without the
// leading 'k' CopyData byte) for ParsePrimaryKeepaliveMessage.
func encodeTestKeepalive(serverWALEnd pglogrepl.LSN, replyRequested bool) []byte {
	buf := make([]byte, 17)
	binary.BigEndian.PutUint64(buf[0:8], uint64(serverWALEnd))
	binary.BigEndian.PutUint64(buf[8:16], 0)
	if replyRequested {
		buf[16] = 1
	}
	return buf
}

func applyTestKeepalive(t *testing.T, c *PostgresConnector, st *walReceiveState, serverWALEnd pglogrepl.LSN, replyRequested bool) (ack pglogrepl.LSN, reply bool) {
	t.Helper()
	pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(encodeTestKeepalive(serverWALEnd, replyRequested))
	if err != nil {
		t.Fatalf("ParsePrimaryKeepaliveMessage: %v", err)
	}
	ack, reply, err = c.applyKeepalive(context.Background(), st, pkm)
	if err != nil {
		t.Fatalf("applyKeepalive: %v", err)
	}
	return ack, reply
}

func TestKeepaliveOnlyAdvancesCheckpointAndAck(t *testing.T) {
	c, _, store := newStreamConnector(t)
	st := &walReceiveState{lastSavedLSN: pglogrepl.LSN(100), clientXLogPos: pglogrepl.LSN(100)}

	ack, reply := applyTestKeepalive(t, c, st, pglogrepl.LSN(250), true)
	if !reply {
		t.Fatal("expected reply after idle keepalive advance")
	}
	if ack != pglogrepl.LSN(250) {
		t.Fatalf("ack=%d want 250", ack)
	}
	if st.lastSavedLSN != pglogrepl.LSN(250) {
		t.Fatalf("lastSavedLSN=%d want 250", st.lastSavedLSN)
	}
	if len(store.saves) != 1 || store.saves[0] != pglogrepl.LSN(250).String() {
		t.Fatalf("saves=%v want [%q]", store.saves, pglogrepl.LSN(250).String())
	}

	ack, reply = applyTestKeepalive(t, c, st, pglogrepl.LSN(400), false)
	if !reply {
		t.Fatal("idle advance must request standby reply even without ReplyRequested")
	}
	if ack != pglogrepl.LSN(400) || st.lastSavedLSN != pglogrepl.LSN(400) {
		t.Fatalf("ack=%d lastSaved=%d want 400", ack, st.lastSavedLSN)
	}
	if len(store.saves) != 2 {
		t.Fatalf("saves=%d want 2", len(store.saves))
	}
}

func TestKeepaliveInFlightCommitCappedByLastSavedLSN(t *testing.T) {
	c, el, store := newStreamConnector(t)
	st := &walReceiveState{lastSavedLSN: pglogrepl.LSN(100), clientXLogPos: pglogrepl.LSN(100)}
	lsn := pglogrepl.LSN(0x50)
	pos := pglogrepl.LSN(0x10)

	applyWAL(t, c, st, pos, encodeStreamRelation())
	pos += 16
	applyWAL(t, c, st, pos, encodeTestBegin(lsn))
	if !st.txOpen {
		t.Fatal("Begin must mark txOpen")
	}
	pos += 16
	applyWAL(t, c, st, pos, encodeStreamInsert("1"))
	if len(st.walBuf) != 1 {
		t.Fatalf("expected buffered insert, got %d", len(st.walBuf))
	}
	savesBefore := len(store.saves)

	ack, reply := applyTestKeepalive(t, c, st, pglogrepl.LSN(999), true)
	if !reply {
		t.Fatal("ReplyRequested must still trigger reply")
	}
	if ack != pglogrepl.LSN(100) {
		t.Fatalf("in-flight ack=%d want lastSavedLSN 100", ack)
	}
	if st.lastSavedLSN != pglogrepl.LSN(100) {
		t.Fatalf("lastSavedLSN advanced to %d during in-flight tx", st.lastSavedLSN)
	}
	if len(store.saves) != savesBefore {
		t.Fatalf("keepalive must not checkpoint during in-flight tx, saves=%v", store.saves)
	}
	if len(el.events) != 0 {
		t.Fatal("insert must still be waiting for Commit")
	}

	pos += 16
	_, committed := applyWAL(t, c, st, pos, encodeTestCommit(lsn))
	if !committed {
		t.Fatal("published Commit must checkpoint")
	}
	if len(el.events) != 1 {
		t.Fatalf("got %d events want 1", len(el.events))
	}
	if st.txOpen {
		t.Fatal("Commit must clear txOpen")
	}
	if st.lastSavedLSN != st.clientXLogPos {
		t.Fatalf("after Commit lastSaved=%d client=%d", st.lastSavedLSN, st.clientXLogPos)
	}
	if len(store.saves) != savesBefore+1 {
		t.Fatalf("Commit should add one save, saves=%v", store.saves)
	}
}

func TestKeepaliveBlockedWhileStreamedOpen(t *testing.T) {
	c, _, store := newStreamConnector(t)
	st := &walReceiveState{lastSavedLSN: pglogrepl.LSN(50), clientXLogPos: pglogrepl.LSN(50)}
	pos := pglogrepl.LSN(0x100)

	applyWAL(t, c, st, pos, encodeStreamRelation())
	applyWAL(t, c, st, pos, encodeTestStreamStart(1))
	if !st.streamedOpen {
		t.Fatal("StreamStart must set streamedOpen")
	}
	applyWAL(t, c, st, pos, encodeInStreamInsert("7"))
	savesBefore := len(store.saves)

	ack, _ := applyTestKeepalive(t, c, st, pglogrepl.LSN(900), true)
	if ack != pglogrepl.LSN(50) {
		t.Fatalf("streamed in-flight ack=%d want 50", ack)
	}
	if st.lastSavedLSN != pglogrepl.LSN(50) || len(store.saves) != savesBefore {
		t.Fatalf("must not advance during streamed tx: lastSaved=%d saves=%v", st.lastSavedLSN, store.saves)
	}
}

func TestKeepaliveBlockedWithBufferedEventsOnly(t *testing.T) {
	c, _, store := newStreamConnector(t)
	st := &walReceiveState{
		lastSavedLSN:  pglogrepl.LSN(10),
		clientXLogPos: pglogrepl.LSN(10),
		walBuf:        []*event.ChangeEvent{{}},
	}
	ack, _ := applyTestKeepalive(t, c, st, pglogrepl.LSN(80), true)
	if ack != pglogrepl.LSN(10) {
		t.Fatalf("buffered ack=%d want 10", ack)
	}
	if len(store.saves) != 0 {
		t.Fatalf("unexpected saves %v", store.saves)
	}
}

func TestIdleForSlotAdvanceFailClosed(t *testing.T) {
	cases := []struct {
		name string
		st   walReceiveState
		want bool
	}{
		{"empty", walReceiveState{}, true},
		{"buf", walReceiveState{walBuf: []*event.ChangeEvent{{}}}, false},
		{"stream", walReceiveState{streamedOpen: true}, false},
		{"tx", walReceiveState{txOpen: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.st.idleForSlotAdvance(); got != tc.want {
				t.Fatalf("idleForSlotAdvance()=%v want %v", got, tc.want)
			}
		})
	}
}
