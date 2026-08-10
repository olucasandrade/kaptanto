package event

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// IDGenerator produces ULIDs. It is safe for concurrent use from multiple
// goroutines.
//
// Monotonic ordering is preserved on a single entropy source guarded by a mutex;
// ULIDs are millisecond-ordered by timestamp and carry a per-source idempotency
// key for deduplication when strict ID ordering matters.
type IDGenerator struct {
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
}

// NewIDGenerator creates a new IDGenerator backed by monotonic entropy.
func NewIDGenerator() *IDGenerator {
	return &IDGenerator{
		entropy: ulid.Monotonic(rand.Reader, 0),
	}
}

// New generates a new ULID. Safe for concurrent use.
func (g *IDGenerator) New() ulid.ULID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), g.entropy)
}
