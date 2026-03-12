package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens a SQLite database at dir/name.db with WAL mode.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cursors.db")
	dsn := fmt.Sprintf("file://%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestCursorStoreDirtyMapFastPath verifies that SaveCursor writes to the dirty
// map (not SQLite) and that LoadCursor returns the saved value before any flush.
func TestCursorStoreDirtyMapFastPath(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSQLiteCursorStore(db, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSQLiteCursorStore: %v", err)
	}

	ctx := context.Background()
	const (
		consumerID  = "consumer-a"
		partitionID = uint32(0)
		seq         = uint64(42)
	)

	if err := store.SaveCursor(ctx, consumerID, partitionID, seq); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	// Before any flush, the value should be returned from the dirty map.
	got, err := store.LoadCursor(ctx, consumerID, partitionID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if got != seq {
		t.Errorf("LoadCursor = %d, want %d", got, seq)
	}

	// Verify it is NOT yet in SQLite by checking the raw DB directly.
	var dbSeq uint64
	err = db.QueryRowContext(ctx,
		"SELECT seq FROM consumer_cursors WHERE consumer_id=? AND partition_id=?",
		consumerID, int(partitionID),
	).Scan(&dbSeq)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows from SQLite before flush, got err=%v seq=%d", err, dbSeq)
	}
}

// TestCursorStoreDefaultReturnsOne verifies that LoadCursor returns 1 (not 0)
// when no cursor has been saved for a (consumerID, partitionID) pair.
func TestCursorStoreDefaultReturnsOne(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSQLiteCursorStore(db, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSQLiteCursorStore: %v", err)
	}

	ctx := context.Background()
	got, err := store.LoadCursor(ctx, "nonexistent-consumer", 99)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if got != 1 {
		t.Errorf("LoadCursor (unknown) = %d, want 1", got)
	}
}

// TestCursorStoreFlushPersistsToSQLite verifies that after a manual flush,
// LoadCursor reads from SQLite and returns the correct value.
func TestCursorStoreFlushPersistsToSQLite(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSQLiteCursorStore(db, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSQLiteCursorStore: %v", err)
	}

	ctx := context.Background()
	const (
		consumerID  = "consumer-b"
		partitionID = uint32(1)
		seq         = uint64(100)
	)

	if err := store.SaveCursor(ctx, consumerID, partitionID, seq); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	// Flush manually.
	store.flush(ctx)

	// Clear dirty map manually to force reading from SQLite.
	store.mu.Lock()
	store.dirty = make(map[cursorKey]uint64)
	store.mu.Unlock()

	got, err := store.LoadCursor(ctx, consumerID, partitionID)
	if err != nil {
		t.Fatalf("LoadCursor after flush: %v", err)
	}
	if got != seq {
		t.Errorf("LoadCursor after flush = %d, want %d", got, seq)
	}
}

// TestCursorStoreIdempotentSave verifies that calling SaveCursor twice for the
// same (consumerID, partitionID) keeps only the latest seq.
func TestCursorStoreIdempotentSave(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSQLiteCursorStore(db, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSQLiteCursorStore: %v", err)
	}

	ctx := context.Background()
	const (
		consumerID  = "consumer-c"
		partitionID = uint32(2)
	)

	if err := store.SaveCursor(ctx, consumerID, partitionID, 10); err != nil {
		t.Fatalf("SaveCursor(10): %v", err)
	}
	if err := store.SaveCursor(ctx, consumerID, partitionID, 20); err != nil {
		t.Fatalf("SaveCursor(20): %v", err)
	}

	got, err := store.LoadCursor(ctx, consumerID, partitionID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if got != 20 {
		t.Errorf("LoadCursor (idempotent) = %d, want 20", got)
	}
}

// TestCursorStoreRunFlushesDirtyOnShutdown verifies that Run flushes dirty
// cursors when the context is cancelled (final shutdown flush).
func TestCursorStoreRunFlushesDirtyOnShutdown(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSQLiteCursorStore(db, 1*time.Hour) // large interval, no ticker flush
	if err != nil {
		t.Fatalf("NewSQLiteCursorStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	const (
		consumerID  = "consumer-d"
		partitionID = uint32(3)
		seq         = uint64(77)
	)

	if err := store.SaveCursor(ctx, consumerID, partitionID, seq); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	// Start Run and cancel immediately.
	done := make(chan struct{})
	go func() {
		store.Run(ctx)
		close(done)
	}()

	cancel()
	<-done // wait for Run to return

	// Dirty map should now be empty and SQLite should have the value.
	store.mu.Lock()
	dirtyLen := len(store.dirty)
	store.mu.Unlock()

	if dirtyLen != 0 {
		t.Errorf("dirty map len = %d after shutdown flush, want 0", dirtyLen)
	}

	// Verify SQLite has the flushed value.
	var dbSeq uint64
	err = db.QueryRowContext(context.Background(),
		"SELECT seq FROM consumer_cursors WHERE consumer_id=? AND partition_id=?",
		consumerID, int(partitionID),
	).Scan(&dbSeq)
	if err != nil {
		t.Fatalf("SQLite query after shutdown flush: %v", err)
	}
	if dbSeq != seq {
		t.Errorf("SQLite seq after shutdown = %d, want %d", dbSeq, seq)
	}
}
