package pgoutput

import "sync"

// TOASTCache stores complete row snapshots for TOAST merging.
// When Postgres omits an unchanged large column value (DataType == 'u'),
// the parser merges the cached value from the last complete row for that key.
//
// Key: toastKey{RelationID, PK} where PK is the JSON-marshaled primary key.
// Value: map[string]any of the complete decoded row.
//
// Safe for concurrent use.
type TOASTCache struct {
	mu    sync.RWMutex
	store map[toastKey]map[string]any
}

// NewTOASTCache creates an empty TOASTCache.
func NewTOASTCache() *TOASTCache {
	return &TOASTCache{
		store: make(map[toastKey]map[string]any),
	}
}

// Set stores or replaces the row snapshot for the given key.
func (c *TOASTCache) Set(key toastKey, row map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = row
}

// Get retrieves the cached row for the given key.
// Returns (nil, false) if not found.
func (c *TOASTCache) Get(key toastKey) (map[string]any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	row, ok := c.store[key]
	return row, ok
}

// Delete evicts the cache entry for the given key.
// Called on DELETE events to prevent stale TOAST merges.
func (c *TOASTCache) Delete(key toastKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.store, key)
}
