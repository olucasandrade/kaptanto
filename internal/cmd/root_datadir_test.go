package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureDataDirMode asserts the runtime state directory is created with
// owner-only permissions (0o700). The directory holds captured CDC row data, so
// it must not be world-traversable on a shared host.
func TestEnsureDataDirMode(t *testing.T) {
	t.Run("creates_with_700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		if err := ensureDataDir(dir); err != nil {
			t.Fatalf("ensureDataDir: %v", err)
		}
		assertMode(t, dir, 0o700)
	})

	t.Run("tightens_preexisting_loose_permissions", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := ensureDataDir(dir); err != nil {
			t.Fatalf("ensureDataDir: %v", err)
		}
		assertMode(t, dir, 0o700)
	})
}

func assertMode(t *testing.T, dir string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("data dir mode = %o, want %o", got, want)
	}
}
