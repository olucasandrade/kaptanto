package event

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// IDGenerator produces monotonically increasing ULIDs.
// It is safe for concurrent use from multiple goroutines.
//
// A single shared MonotonicEntropy source ensures that two IDs generated
// within the same millisecond are still ordered (pitfall 2 from research).
type IDGenerator struct {
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
}

// NewIDGenerator creates a new IDGenerator backed by a monotonic entropy source.
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
