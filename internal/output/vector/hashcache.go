package vector

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	// Register the pure-Go "sqlite" driver (CGO_ENABLED=0).
	_ "modernc.org/sqlite"
)

const (
	hashCacheSchema = `
CREATE TABLE IF NOT EXISTS hashes (
    id         TEXT PRIMARY KEY,
    hash       BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);`

	defaultHashLRUCap = 4096
)

// HashCache is the VEC-01 SHA-256 text-hash cache backed by SQLite.
// An in-process LRU sits in front of SQLite on the Deliver hot path.
// Best-effort: OpenHashCache never returns an error — open/schema failure
// disables the cache (every event embeds; correct but costlier).
type HashCache struct {
	db       *sql.DB
	disabled bool
	mu       sync.Mutex
	lru      *hashLRU
}

type hashLRU struct {
	cap   int
	order *list.List
	items map[string]*list.Element
}

type hashLRUEntry struct {
	id   string
	hash []byte
}

func newHashLRU(cap int) *hashLRU {
	if cap <= 0 {
		cap = defaultHashLRUCap
	}
	return &hashLRU{
		cap:   cap,
		order: list.New(),
		items: make(map[string]*list.Element),
	}
}

func (l *hashLRU) get(id string) ([]byte, bool) {
	el, ok := l.items[id]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(el)
	entry := el.Value.(*hashLRUEntry)
	out := make([]byte, len(entry.hash))
	copy(out, entry.hash)
	return out, true
}

func (l *hashLRU) put(id string, hash []byte) {
	if el, ok := l.items[id]; ok {
		entry := el.Value.(*hashLRUEntry)
		entry.hash = appendHash(entry.hash[:0], hash)
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&hashLRUEntry{
		id:   id,
		hash: appendHash(nil, hash),
	})
	l.items[id] = el
	for l.order.Len() > l.cap {
		back := l.order.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*hashLRUEntry)
		delete(l.items, evicted.id)
		l.order.Remove(back)
	}
}

func (l *hashLRU) del(id string) {
	el, ok := l.items[id]
	if !ok {
		return
	}
	delete(l.items, id)
	l.order.Remove(el)
}

func appendHash(dst, hash []byte) []byte {
	dst = append(dst[:0], hash...)
	if cap(dst) > len(dst) {
		// Keep a tight backing array for LRU entries.
		compact := make([]byte, len(dst))
		copy(compact, dst)
		return compact
	}
	return dst
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
	db.SetMaxOpenConns(1)
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
	return &HashCache{db: db, lru: newHashLRU(defaultHashLRUCap)}
}

// Disabled reports whether the cache is inactive (VEC-01 best-effort).
func (c *HashCache) Disabled() bool {
	return c == nil || c.disabled || c.db == nil
}

// HashText returns the SHA-256 digest of text.
func HashText(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

// Get returns the stored hash for id. ok is false when missing or disabled.
func (c *HashCache) Get(id string) (hash []byte, ok bool) {
	if c.Disabled() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hash, ok := c.lru.get(id); ok {
		return hash, true
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
	c.lru.put(id, stored)
	out := make([]byte, len(stored))
	copy(out, stored)
	return out, true
}

// Unchanged reports whether Get(id) equals hash (VEC-01 skip-embed condition).
func (c *HashCache) Unchanged(id string, hash []byte) bool {
	stored, ok := c.Get(id)
	if !ok {
		return false
	}
	return bytes.Equal(stored, hash)
}

// Put upserts id → hash. No-op when disabled.
func (c *HashCache) Put(id string, hash []byte) error {
	return c.PutBatch([]string{id}, [][]byte{hash})
}

// PutBatch upserts many id→hash pairs in one SQLite transaction.
func (c *HashCache) PutBatch(ids []string, hashes [][]byte) error {
	if c.Disabled() || len(ids) == 0 {
		return nil
	}
	if len(ids) != len(hashes) {
		return fmt.Errorf("vector: hash cache put batch: %d ids vs %d hashes", len(ids), len(hashes))
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("vector: hash cache put batch begin: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO hashes (id, hash, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET hash = excluded.hash, updated_at = excluded.updated_at`,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("vector: hash cache put batch prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := stmt.Exec(id, hashes[i], now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("vector: hash cache put %q: %w", id, err)
		}
		c.lru.put(id, hashes[i])
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vector: hash cache put batch commit: %w", err)
	}
	return nil
}

// Del removes id from the cache. No-op when disabled.
func (c *HashCache) Del(id string) error {
	return c.DelBatch([]string{id})
}

// DelBatch removes many ids in one SQLite transaction.
func (c *HashCache) DelBatch(ids []string) error {
	if c.Disabled() || len(ids) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("vector: hash cache del batch begin: %w", err)
	}
	stmt, err := tx.Prepare(`DELETE FROM hashes WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("vector: hash cache del batch prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("vector: hash cache del %q: %w", id, err)
		}
		c.lru.del(id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vector: hash cache del batch commit: %w", err)
	}
	return nil
}

// Close releases the SQLite handle. Safe on a disabled cache.
func (c *HashCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.db.Close()
	c.db = nil
	c.disabled = true
	c.lru = nil
	if err != nil {
		return fmt.Errorf("vector: hash cache close: %w", err)
	}
	return nil
}
