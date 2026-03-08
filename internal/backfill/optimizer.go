package backfill

import "time"

const (
	defaultBatch = 5_000
	minBatch     = 100
	maxBatch     = 50_000
)

// BatchOptimizer adaptively adjusts the snapshot batch size based on observed
// query durations. It starts at defaultBatch and adjusts within [minBatch, maxBatch].
type BatchOptimizer struct {
	current int
}

// NewBatchOptimizer returns a BatchOptimizer starting at the default batch size (5000).
func NewBatchOptimizer() *BatchOptimizer {
	return &BatchOptimizer{current: defaultBatch}
}

// Adjust updates and returns the new batch size based on the observed query duration.
//
//   - d < 1s:  grow by 25% (capped at 50000)
//   - d > 5s:  shrink by 50% (floored at 100)
//   - d > 3s:  shrink by 50% (floored at 100)
//   - 1s–3s:   no change
func (o *BatchOptimizer) Adjust(d time.Duration) int {
	switch {
	case d < time.Second:
		o.current = min(int(float64(o.current)*1.25), maxBatch)
	case d > 5*time.Second:
		o.current = max(o.current/2, minBatch)
	case d > 3*time.Second:
		o.current = max(o.current/2, minBatch)
	}
	return o.current
}

// Current returns the current batch size without adjusting it.
func (o *BatchOptimizer) Current() int { return o.current }
