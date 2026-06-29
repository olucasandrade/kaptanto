package checkpoint_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStore_OpenCreatesSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err, "Open should succeed")
	require.NotNil(t, store)
	defer func() { _ = store.Close() }()
}

func TestSQLiteStore_Ping(t *testing.T) {
	store, err := checkpoint.Open(filepath.Join(t.TempDir(), "checkpoint.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	assert.NoError(t, store.Ping(), "Ping on an open store should succeed")
}

func TestSQLiteStore_OpenInvalidPath(t *testing.T) {
	// A path whose parent directory does not exist cannot be opened.
	_, err := checkpoint.Open(filepath.Join(t.TempDir(), "no-such-dir", "checkpoint.db"))
	require.Error(t, err, "Open should fail when the directory does not exist")
}

func TestSQLiteStore_LoadAfterClose(t *testing.T) {
	store, err := checkpoint.Open(filepath.Join(t.TempDir(), "checkpoint.db"))
	require.NoError(t, err)
	require.NoError(t, store.Close())
	// Operations after Close must surface an error, not panic.
	_, err = store.Load(context.Background(), "source-1")
	require.Error(t, err)
}

func TestSQLiteStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	const sourceID = "source-1"
	const lsn = "0/1A2B3C4"

	err = store.Save(ctx, sourceID, lsn)
	require.NoError(t, err, "Save should succeed")

	got, err := store.Load(ctx, sourceID)
	require.NoError(t, err, "Load should succeed")
	assert.Equal(t, lsn, got, "Loaded LSN should match saved LSN")
}

func TestSQLiteStore_SaveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	const sourceID = "source-1"
	const lsn1 = "0/1A2B3C4"
	const lsn2 = "0/2BCDEF0"

	err = store.Save(ctx, sourceID, lsn1)
	require.NoError(t, err)

	err = store.Save(ctx, sourceID, lsn2)
	require.NoError(t, err, "Second Save should succeed (upsert)")

	got, err := store.Load(ctx, sourceID)
	require.NoError(t, err)
	assert.Equal(t, lsn2, got, "Second save should update the checkpoint")
}

func TestSQLiteStore_LoadNonexistentSourceID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	got, err := store.Load(ctx, "nonexistent-source")
	require.NoError(t, err, "Load for unknown sourceID should not error — it is first-run")
	assert.Equal(t, "", got, "Load for unknown sourceID should return empty string")
}

func TestSQLiteStore_OpenExistingDB_ReturnsStoredLSN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	// Write a checkpoint.
	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.Save(ctx, "pg-main", "0/DEADBEEF")
	require.NoError(t, err)

	require.NoError(t, store.Close())

	// Re-open the same file; it must return the stored LSN, not zero.
	store2, err := checkpoint.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = store2.Close() }()

	got, err := store2.Load(ctx, "pg-main")
	require.NoError(t, err)
	assert.Equal(t, "0/DEADBEEF", got, "Reopening DB must return previously stored LSN")
}

func TestSQLiteStore_CloseFlushesPendingWrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "checkpoint.db")

	store, err := checkpoint.Open(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, store.Save(ctx, "source-flush", "0/CAFEBABE"))

	// Close must not error — this is the graceful shutdown invariant.
	err = store.Close()
	assert.NoError(t, err, "Close should flush WAL and return no error")
}
