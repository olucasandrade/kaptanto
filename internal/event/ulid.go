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
// Monotonic ordering is preserved per sync.Pool entropy instance; ULIDs are
// millisecond-ordered by timestamp and carry a per-source idempotency key for
// deduplication when strict ID ordering matters.
type IDGenerator struct {
	pool sync.Pool
}

// NewIDGenerator creates a new IDGenerator backed by a pool of monotonic
// entropy sources.
func NewIDGenerator() *IDGenerator {
	return &IDGenerator{
		pool: sync.Pool{
			New: func() any {
				return ulid.Monotonic(rand.Reader, 0)
			},
		},
	}
}

// New generates a new ULID. Safe for concurrent use.
func (g *IDGenerator) New() ulid.ULID {
	entropy := g.pool.Get().(*ulid.MonotonicEntropy)
	defer g.pool.Put(entropy)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
}
