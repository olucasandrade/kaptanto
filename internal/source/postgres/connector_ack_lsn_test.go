package postgres

import (
	"testing"

	"github.com/jackc/pglogrepl"
)

func TestAckLSN(t *testing.T) {
	saved := pglogrepl.LSN(100)
	if got := ackLSN(50, saved); got != 50 {
		t.Fatalf("ackLSN(50, 100) = %d, want 50", got)
	}
	if got := ackLSN(150, saved); got != saved {
		t.Fatalf("ackLSN(150, 100) = %d, want %d", got, saved)
	}
	if got := ackLSN(saved, saved); got != saved {
		t.Fatalf("ackLSN(100, 100) = %d, want %d", got, saved)
	}
}
