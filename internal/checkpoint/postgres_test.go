package checkpoint_test

import (
	"context"
	"os"
	"testing"

	"github.com/kaptanto/kaptanto/internal/checkpoint"
)

// TestPostgresStore runs integration tests for PostgresStore.
// Set POSTGRES_TEST_DSN to a valid Postgres DSN to run these tests.
// Example: POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/testdb" go test ./internal/checkpoint/...
func TestPostgresStore_LoadEmpty(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}

	ctx := context.Background()
	store, err := checkpoint.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	// First run: no row exists, Load must return ("", nil)
	lsn, err := store.Load(ctx, "test-source-empty")
	if err != nil {
		t.Fatalf("Load on empty table: expected nil error, got %v", err)
	}
	if lsn != "" {
		t.Fatalf("Load on empty table: expected empty string, got %q", lsn)
	}
}

func TestPostgresStore_SaveAndLoad(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}

	ctx := context.Background()
	store, err := checkpoint.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	sourceID := "test-source-save-load"
	wantLSN := "0/1A2B3C4"

	if err := store.Save(ctx, sourceID, wantLSN); err != nil {
		t.Fatalf("Save: %v", err)
	}

	gotLSN, err := store.Load(ctx, sourceID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotLSN != wantLSN {
		t.Fatalf("Load returned %q, want %q", gotLSN, wantLSN)
	}
}

func TestPostgresStore_SaveTwiceUpdates(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}

	ctx := context.Background()
	store, err := checkpoint.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	sourceID := "test-source-double-save"
	firstLSN := "0/AAAAAA"
	secondLSN := "0/BBBBBB"

	if err := store.Save(ctx, sourceID, firstLSN); err != nil {
		t.Fatalf("First Save: %v", err)
	}
	if err := store.Save(ctx, sourceID, secondLSN); err != nil {
		t.Fatalf("Second Save: %v", err)
	}

	gotLSN, err := store.Load(ctx, sourceID)
	if err != nil {
		t.Fatalf("Load after double save: %v", err)
	}
	if gotLSN != secondLSN {
		t.Fatalf("Load returned %q after double save, want %q", gotLSN, secondLSN)
	}
}

func TestPostgresStore_InterfaceSatisfied(t *testing.T) {
	// Compile-time check: PostgresStore satisfies CheckpointStore
	// This test is trivially satisfied if the package compiles.
	// The real assertion is the var _ CheckpointStore = (*PostgresStore)(nil) line in postgres.go.
	t.Log("PostgresStore interface check: compile-time assertion in postgres.go verifies this")
}
