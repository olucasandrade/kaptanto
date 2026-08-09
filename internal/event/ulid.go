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
// Monotonic ordering within a single source/worker is preserved by giving each
// goroutine its own MonotonicEntropy source via sync.Pool. Global monotonic
// ordering is not required because each event already carries a source-scoped
// idempotency key for deduplication.
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
