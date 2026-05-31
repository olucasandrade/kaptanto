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
	dir := filepath.Join(t.TempDir(), "data")
	if err := ensureDataDir(dir); err != nil {
		t.Fatalf("ensureDataDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("data dir mode = %o, want 700", got)
	}
}
