package pgoutput

import (
	"container/list"
	"sync"
)

const defaultToastCacheCap = 4096

// TOASTCache stores complete row snapshots for TOAST merging.
// When Postgres omits an unchanged large column value (DataType == 'u'),
// the parser merges the cached value from the last complete row for that key.
//
// Key: toastKey{RelationID, PK} where PK is the JSON-marshaled primary key.
// Value: map[string]any of the complete decoded row.
//
// An LRU cap prevents unbounded growth on wide tables with many hot keys.
// Safe for concurrent use.
type TOASTCache struct {
	mu    sync.RWMutex
	store map[toastKey]map[string]any
	order *list.List
	items map[toastKey]*list.Element
	cap   int
}

type toastLRUEntry struct {
	key toastKey
	row map[string]any
}

// NewTOASTCache creates an empty TOASTCache with the default LRU capacity.
func NewTOASTCache() *TOASTCache {
	return &TOASTCache{
		store: make(map[toastKey]map[string]any),
		order: list.New(),
		items: make(map[toastKey]*list.Element),
		cap:   defaultToastCacheCap,
	}
}

// Set stores or replaces the row snapshot for the given key.
func (c *TOASTCache) Set(key toastKey, row map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		entry := el.Value.(*toastLRUEntry)
		entry.row = row
		c.store[key] = row
		c.order.MoveToFront(el)
		return
	}

	el := c.order.PushFront(&toastLRUEntry{key: key, row: row})
	c.items[key] = el
	c.store[key] = row

	for c.order.Len() > c.cap {
		back := c.order.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*toastLRUEntry)
		c.order.Remove(back)
		delete(c.items, evicted.key)
		delete(c.store, evicted.key)
	}
}

// Get retrieves the cached row for the given key.
// Returns (nil, false) if not found.
func (c *TOASTCache) Get(key toastKey) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.store[key]
	if !ok {
		return nil, false
	}
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
	}
	return row, true
}

// Delete evicts the cache entry for the given key.
// Called on DELETE events to prevent stale TOAST merges.
func (c *TOASTCache) Delete(key toastKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
		delete(c.items, key)
	}
	delete(c.store, key)
}
