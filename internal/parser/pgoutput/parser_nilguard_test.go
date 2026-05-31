package pgoutput

// White-box tests for the DML handlers' nil-tuple guards.
//
// pglogrepl's wire decoder rejects DML messages that carry no tuple (it returns
// an error before the handler runs), so these edge cases cannot be reproduced
// through the public Parse path. We therefore exercise the handlers directly to
// prove they return a descriptive error rather than panicking on a nil tuple —
// the failure mode a schema skew or a REPLICA IDENTITY NOTHING table could
// trigger. A panic here would abort the whole process (parse errors are
// non-fatal in the connector, but a panic is not recovered).

import (
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/olucasandrade/kaptanto/internal/event"
)

func newNilGuardParser(t *testing.T) *Parser {
	t.Helper()
	p := New("test-source", event.NewIDGenerator())
	p.relations.Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   42,
			Namespace:    "public",
			RelationName: "widgets",
			Columns: []*pglogrepl.RelationMessageColumn{
				{Name: "id", DataType: 23, Flags: 1},
			},
		},
	})
	return p
}

func TestHandleInsertNilTupleReturnsError(t *testing.T) {
	p := newNilGuardParser(t)
	ev, err := p.handleInsert(&pglogrepl.InsertMessageV2{
		InsertMessage: pglogrepl.InsertMessage{RelationID: 42, Tuple: nil},
	})
	if err == nil {
		t.Fatalf("expected error for nil tuple, got event %+v", ev)
	}
	if ev != nil {
		t.Fatalf("expected nil event on error, got %+v", ev)
	}
}

func TestHandleUpdateNilNewTupleReturnsError(t *testing.T) {
	p := newNilGuardParser(t)
	ev, err := p.handleUpdate(&pglogrepl.UpdateMessageV2{
		UpdateMessage: pglogrepl.UpdateMessage{RelationID: 42, NewTuple: nil},
	})
	if err == nil {
		t.Fatalf("expected error for nil new tuple, got event %+v", ev)
	}
	if ev != nil {
		t.Fatalf("expected nil event on error, got %+v", ev)
	}
}

func TestHandleDeleteNilOldTupleReturnsError(t *testing.T) {
	p := newNilGuardParser(t)
	ev, err := p.handleDelete(&pglogrepl.DeleteMessageV2{
		DeleteMessage: pglogrepl.DeleteMessage{RelationID: 42, OldTuple: nil},
	})
	if err == nil {
		t.Fatalf("expected error for nil old tuple, got event %+v", ev)
	}
	if ev != nil {
		t.Fatalf("expected nil event on error, got %+v", ev)
	}
}
