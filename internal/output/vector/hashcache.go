package vector

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	// Register the pure-Go "sqlite" driver (CGO_ENABLED=0).
	_ "modernc.org/sqlite"
)

const hashCacheSchema = `
CREATE TABLE IF NOT EXISTS hashes (
    id         TEXT PRIMARY KEY,
    hash       BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);`

// HashCache is the VEC-01 SHA-256 text-hash cache backed by SQLite.
// Best-effort: OpenHashCache never returns an error — open/schema failure
// disables the cache (every event embeds; correct but costlier).
type HashCache struct {
	db       *sql.DB
	disabled bool
}

// OpenHashCache opens (or creates) <dataDir>/vector-hashes.db in WAL mode.
// On any failure it logs a warning and returns a disabled cache.
func OpenHashCache(dataDir string) *HashCache {
	path := filepath.Join(dataDir, "vector-hashes.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		slog.Warn("vector: hash cache disabled (open failed)", "path", path, "err", err)
		return &HashCache{disabled: true}
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			slog.Warn("vector: hash cache disabled (pragma failed)", "path", path, "err", err)
			return &HashCache{disabled: true}
		}
	}
	if _, err := db.Exec(hashCacheSchema); err != nil {
		_ = db.Close()
		slog.Warn("vector: hash cache disabled (schema failed)", "path", path, "err", err)
		return &HashCache{disabled: true}
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		slog.Warn("vector: hash cache disabled (ping failed)", "path", path, "err", err)
		return &HashCache{disabled: true}
	}
	return &HashCache{db: db}
}

// Disabled reports whether the cache is inactive (VEC-01 best-effort).
func (c *HashCache) Disabled() bool {
	return c == nil || c.disabled || c.db == nil
}

// HashText returns the SHA-256 digest of text.
func HashText(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	out := make([]byte, len(sum))
	copy(out, sum[:])
	return out
}

// Get returns the stored hash for id. ok is false when missing or disabled.
func (c *HashCache) Get(id string) (hash []byte, ok bool) {
	if c.Disabled() {
		return nil, false
	}
	var stored []byte
	err := c.db.QueryRow(`SELECT hash FROM hashes WHERE id = ?`, id).Scan(&stored)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		slog.Warn("vector: hash cache get failed; treating as miss", "id", id, "err", err)
		return nil, false
	}
	return stored, true
}

// Unchanged reports whether Get(id) equals hash (VEC-01 skip-embed condition).
func (c *HashCache) Unchanged(id string, hash []byte) bool {
	stored, ok := c.Get(id)
	if !ok {
		return false
	}
	if len(stored) != len(hash) {
		return false
	}
	for i := range stored {
		if stored[i] != hash[i] {
			return false
		}
	}
	return true
}

// Put upserts id → hash. No-op when disabled.
func (c *HashCache) Put(id string, hash []byte) error {
	if c.Disabled() {
		return nil
	}
	_, err := c.db.Exec(
		`INSERT INTO hashes (id, hash, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET hash = excluded.hash, updated_at = excluded.updated_at`,
		id, hash, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("vector: hash cache put %q: %w", id, err)
	}
	return nil
}

// Del removes id from the cache. No-op when disabled.
func (c *HashCache) Del(id string) error {
	if c.Disabled() {
		return nil
	}
	_, err := c.db.Exec(`DELETE FROM hashes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("vector: hash cache del %q: %w", id, err)
	}
	return nil
}

// Close releases the SQLite handle. Safe on a disabled cache.
func (c *HashCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	c.disabled = true
	if err != nil {
		return fmt.Errorf("vector: hash cache close: %w", err)
	}
	return nil
}
