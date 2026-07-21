package vector

import "github.com/olucasandrade/kaptanto/internal/observability"

// Export internals for coverage-oriented unit tests.
var (
	FormatValueForTest = formatValue
	SplitRunsForTest   = splitRuns
)

// TextHashCacheForTest exposes the hash-cache interface for external tests.
type TextHashCacheForTest = textHashCache

// MetricsForTest returns the injected metrics (may be nil).
func (c *VectorSinkConsumer) MetricsForTest() *observability.KaptantoMetrics {
	return c.m
}
