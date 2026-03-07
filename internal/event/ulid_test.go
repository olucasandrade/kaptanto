package event_test

import (
	"sync"
	"testing"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIDGenerator_New_ProducesValidULID(t *testing.T) {
	gen := event.NewIDGenerator()
	id := gen.New()

	// A ULID is a 16-byte value; its string form is 26 characters.
	s := id.String()
	assert.Len(t, s, 26, "ULID string should be 26 characters long")
	assert.NotEmpty(t, s)
}

func TestIDGenerator_New_MonotonicallyIncreasing(t *testing.T) {
	gen := event.NewIDGenerator()
	id1 := gen.New()
	id2 := gen.New()

	// ULIDs are lexicographically sortable in time order.
	assert.True(t, id2.String() > id1.String(),
		"second ULID %q should be greater than first ULID %q", id2.String(), id1.String())
}

func TestIDGenerator_New_ConcurrentSafe(t *testing.T) {
	const goroutines = 100
	gen := event.NewIDGenerator()
	ids := make([]string, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			ids[i] = gen.New().String()
		}()
	}
	wg.Wait()

	// All generated IDs must be unique.
	seen := make(map[string]bool, goroutines)
	for _, id := range ids {
		require.NotEmpty(t, id, "generated ULID should not be empty")
		assert.False(t, seen[id], "duplicate ULID detected: %s", id)
		seen[id] = true
	}
}
