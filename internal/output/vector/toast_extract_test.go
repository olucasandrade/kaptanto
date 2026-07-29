package vector_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtract_TOASTMergedUpdateFullText documents case G3-19 #7:
// Postgres TOAST-omitted columns are merged into After by the parser before
// the vector sink sees the event. Extraction must therefore read the full
// post-merge After text (not a truncated/missing TOAST column).
func TestExtract_TOASTMergedUpdateFullText(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title", "body"},
	})
	require.NoError(t, err)

	// Simulate a TOAST-merged UPDATE: After carries the full large body
	// that the WAL message itself would have omitted.
	fullBody := "Lorem ipsum dolor sit amet — restored from TOASTCache"
	text, err := ext.Extract(sampleEvent(map[string]any{
		"title": "Hello",
		"body":  fullBody,
		"views": float64(9), // non-extracted column change only
	}))
	require.NoError(t, err)
	assert.Equal(t, "title: Hello\nbody: "+fullBody, text)
}
