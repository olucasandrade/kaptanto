package pgoutput

import (
	"sync"

	"github.com/jackc/pglogrepl"
)

// RelationCache stores relation (table schema) metadata received from pgoutput
// RelationMessages. It is keyed by uint32 RelationID (OID), not by schema+table
// name — OIDs can change after DROP+CREATE on the same table name.
//
// Must be cleared on every new replication session (connector responsibility).
// Safe for concurrent use.
type RelationCache struct {
	mu    sync.RWMutex
	store map[uint32]*pglogrepl.RelationMessageV2
}

// NewRelationCache creates an empty RelationCache.
func NewRelationCache() *RelationCache {
	return &RelationCache{
		store: make(map[uint32]*pglogrepl.RelationMessageV2),
	}
}

// Set stores or replaces the relation entry for the given RelationID.
func (c *RelationCache) Set(msg *pglogrepl.RelationMessageV2) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[msg.RelationID] = msg
}

// Get retrieves the cached relation for the given RelationID.
// Returns (nil, false) if not found.
func (c *RelationCache) Get(id uint32) (*pglogrepl.RelationMessageV2, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	msg, ok := c.store[id]
	return msg, ok
}
