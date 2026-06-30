package backfill

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresBackfillStore tests the PostgresBackfillStore implementation.
// Tests require a real Postgres instance and skip when TEST_CLUSTER_DSN is unset.
func TestPostgresBackfillStore(t *testing.T) {
	dsn := os.Getenv("TEST_CLUSTER_DSN")
	if dsn == "" {
		t.Skip("skipping: TEST_CLUSTER_DSN not set")
	}

	ctx := context.Background()
	store, err := OpenPostgresBackfillStore(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgresBackfillStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("LoadState returns nil nil for unknown table", func(t *testing.T) {
		state, err := store.LoadState(ctx, "src-unknown", "public.nonexistent")
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if state != nil {
			t.Fatalf("expected nil state, got: %+v", state)
		}
	})

	t.Run("SaveState then LoadState returns equivalent struct", func(t *testing.T) {
		want := &BackfillState{
			SourceID:      "src-1",
			Table:         "public.orders",
			Status:        "running",
			Strategy:      "keyset",
			CursorKey:     []byte(`{"id":42}`),
			TotalRows:     1000,
			ProcessedRows: 500,
			SnapshotLSN:   99887766,
			StartedAt:     time.Now().UTC().Truncate(time.Millisecond),
		}

		if err := store.SaveState(ctx, want); err != nil {
			t.Fatalf("SaveState: %v", err)
		}

		got, err := store.LoadState(ctx, want.SourceID, want.Table)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if got == nil {
			t.Fatal("LoadState returned nil after SaveState")
		}

		if got.SourceID != want.SourceID {
			t.Errorf("SourceID: got %q, want %q", got.SourceID, want.SourceID)
		}
		if got.Table != want.Table {
			t.Errorf("Table: got %q, want %q", got.Table, want.Table)
		}
		if got.Status != want.Status {
			t.Errorf("Status: got %q, want %q", got.Status, want.Status)
		}
		if got.Strategy != want.Strategy {
			t.Errorf("Strategy: got %q, want %q", got.Strategy, want.Strategy)
		}
		if string(got.CursorKey) != string(want.CursorKey) {
			t.Errorf("CursorKey: got %q, want %q", got.CursorKey, want.CursorKey)
		}
		if got.TotalRows != want.TotalRows {
			t.Errorf("TotalRows: got %d, want %d", got.TotalRows, want.TotalRows)
		}
		if got.ProcessedRows != want.ProcessedRows {
			t.Errorf("ProcessedRows: got %d, want %d", got.ProcessedRows, want.ProcessedRows)
		}
		if got.SnapshotLSN != want.SnapshotLSN {
			t.Errorf("SnapshotLSN: got %d, want %d", got.SnapshotLSN, want.SnapshotLSN)
		}
	})

	t.Run("cursor_key binary data survives round-trip", func(t *testing.T) {
		binary := []byte{0x00, 0xFF, 0x42, 0x01, 0xDE, 0xAD}
		state := &BackfillState{
			SourceID:    "src-binary",
			Table:       "public.binary_test",
			Status:      "pending",
			Strategy:    "keyset",
			CursorKey:   binary,
			StartedAt:   time.Now().UTC(),
		}

		if err := store.SaveState(ctx, state); err != nil {
			t.Fatalf("SaveState: %v", err)
		}

		got, err := store.LoadState(ctx, state.SourceID, state.Table)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if got == nil {
			t.Fatal("LoadState returned nil after SaveState")
		}

		if len(got.CursorKey) != len(binary) {
			t.Fatalf("CursorKey length: got %d, want %d", len(got.CursorKey), len(binary))
		}
		for i := range binary {
			if got.CursorKey[i] != binary[i] {
				t.Errorf("CursorKey[%d]: got 0x%02X, want 0x%02X", i, got.CursorKey[i], binary[i])
			}
		}
	})

	t.Run("SaveState upserts on second call", func(t *testing.T) {
		state := &BackfillState{
			SourceID:      "src-upsert",
			Table:         "public.upsert_test",
			Status:        "pending",
			Strategy:      "keyset",
			ProcessedRows: 0,
			StartedAt:     time.Now().UTC(),
		}

		if err := store.SaveState(ctx, state); err != nil {
			t.Fatalf("first SaveState: %v", err)
		}

		state.Status = "done"
		state.ProcessedRows = 999
		if err := store.SaveState(ctx, state); err != nil {
			t.Fatalf("second SaveState: %v", err)
		}

		got, err := store.LoadState(ctx, state.SourceID, state.Table)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if got.Status != "done" {
			t.Errorf("Status after upsert: got %q, want %q", got.Status, "done")
		}
		if got.ProcessedRows != 999 {
			t.Errorf("ProcessedRows after upsert: got %d, want 999", got.ProcessedRows)
		}
	})
}

// TestPostgresBackfillStoreSQLConstants validates that SQL constants in
// postgres_store.go use the correct Postgres syntax (no CGO required).
func TestPostgresBackfillStoreSQLConstants(t *testing.T) {
	// Verify BYTEA (not BLOB) in schema
	if !containsString(createPostgresBackfillTableSQL, "BYTEA") {
		t.Error("createPostgresBackfillTableSQL must use BYTEA (not BLOB) for cursor_key")
	}
	if containsString(createPostgresBackfillTableSQL, "BLOB") {
		t.Error("createPostgresBackfillTableSQL must not contain BLOB (use BYTEA)")
	}

	// Verify TIMESTAMPTZ (not DATETIME) in schema
	if !containsString(createPostgresBackfillTableSQL, "TIMESTAMPTZ") {
		t.Error("createPostgresBackfillTableSQL must use TIMESTAMPTZ (not DATETIME)")
	}
	if containsString(createPostgresBackfillTableSQL, "DATETIME") {
		t.Error("createPostgresBackfillTableSQL must not contain DATETIME (use TIMESTAMPTZ)")
	}

	// Verify $N placeholders (not ?) in upsert
	if !containsString(upsertPostgresBackfillStateSQL, "$1") {
		t.Error("upsertPostgresBackfillStateSQL must use $N placeholders (not ?)")
	}
	if containsString(upsertPostgresBackfillStateSQL, "?,") {
		t.Error("upsertPostgresBackfillStateSQL must not use ? placeholders")
	}

	// Verify ON CONFLICT syntax
	if !containsString(upsertPostgresBackfillStateSQL, "ON CONFLICT") {
		t.Error("upsertPostgresBackfillStateSQL must contain ON CONFLICT")
	}

	// Verify select uses $N
	if !containsString(selectPostgresBackfillStateSQL, "$1") {
		t.Error("selectPostgresBackfillStateSQL must use $N placeholders")
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
