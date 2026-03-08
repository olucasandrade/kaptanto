package postgres_test

import (
	"testing"

	"github.com/kaptanto/kaptanto/internal/source/postgres"
)

// TestDefaultSlotName verifies that slotName defaults to "kaptanto_" + SourceID
// when left empty.
func TestDefaultSlotName(t *testing.T) {
	cfg := postgres.Config{
		DSN:      "postgres://localhost/testdb",
		SourceID: "pg1",
		Tables:   []string{"public.orders"},
	}
	cfg.ApplyDefaults()

	if cfg.SlotName != "kaptanto_pg1" {
		t.Errorf("SlotName = %q, want %q", cfg.SlotName, "kaptanto_pg1")
	}
	if cfg.PublicationName != "kaptanto_pub_pg1" {
		t.Errorf("PublicationName = %q, want %q", cfg.PublicationName, "kaptanto_pub_pg1")
	}
}

// TestDefaultBackoffs verifies that InitialBackoff and MaxBackoff are set
// when the caller leaves them zero.
func TestDefaultBackoffs(t *testing.T) {
	cfg := postgres.Config{
		DSN:      "postgres://localhost/testdb",
		SourceID: "pg1",
	}
	cfg.ApplyDefaults()

	if cfg.InitialBackoff == 0 {
		t.Error("InitialBackoff should be non-zero after ApplyDefaults")
	}
	if cfg.MaxBackoff == 0 {
		t.Error("MaxBackoff should be non-zero after ApplyDefaults")
	}
	if cfg.InitialBackoff >= cfg.MaxBackoff {
		t.Errorf("InitialBackoff (%v) should be less than MaxBackoff (%v)",
			cfg.InitialBackoff, cfg.MaxBackoff)
	}
}

// TestReplicationDSN verifies that the replication DSN appended by the
// connector adds "?replication=database" correctly regardless of whether
// the base DSN already contains query parameters.
func TestReplicationDSN(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		wantSuf string
	}{
		{
			name:    "no existing params",
			dsn:     "postgres://localhost/db",
			wantSuf: "?replication=database",
		},
		{
			name:    "existing params",
			dsn:     "postgres://localhost/db?target_session_attrs=read-write",
			wantSuf: "&replication=database",
		},
		{
			name:    "multi-host",
			dsn:     "postgres://h1,h2/db?target_session_attrs=read-write",
			wantSuf: "&replication=database",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := postgres.BuildReplicationDSN(tc.dsn)
			if len(got) < len(tc.wantSuf) || got[len(got)-len(tc.wantSuf):] != tc.wantSuf {
				t.Errorf("BuildReplicationDSN(%q) = %q, want suffix %q", tc.dsn, got, tc.wantSuf)
			}
		})
	}
}

// TestWasEverConnectedFlag verifies the slot-absent-after-failover detection
// logic: when wasEverConnected=true and the slot is absent, needsSnapshot must
// be set to true.
//
// This is tested via the exported SlotCheckResult helper (no live DB required).
func TestSlotCheckResult(t *testing.T) {
	// Slot present → no snapshot needed regardless of wasEverConnected.
	r1 := postgres.EvalSlotCheck(true, true)
	if r1 {
		t.Error("slotPresent=true: needsSnapshot should be false")
	}

	// Slot absent, first connection → no snapshot (cold start).
	r2 := postgres.EvalSlotCheck(false, false)
	if r2 {
		t.Error("slotPresent=false, wasEverConnected=false: needsSnapshot should be false")
	}

	// Slot absent after successful prior connection → needs snapshot (SRC-06).
	r3 := postgres.EvalSlotCheck(false, true)
	if !r3 {
		t.Error("slotPresent=false, wasEverConnected=true: needsSnapshot should be true")
	}
}

// Integration tests that require a live Postgres are tagged and excluded from
// the default test run. See connector_integration_test.go.
