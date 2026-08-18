package vector

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncate_Boundary(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantMax    int
		wantSuffix string
	}{
		{"short ASCII unchanged", "hello", 512, ""},
		{"exactly max ASCII", strings.Repeat("a", 512), 512, ""},
		{"over max ASCII", strings.Repeat("a", 513), 512, "…"},
		{"over max multibyte", strings.Repeat("é", 300), 512, "…"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input)
			assert.LessOrEqual(t, len(got), tc.wantMax, "result exceeds byte limit")
			assert.True(t, utf8.ValidString(got), "result must be valid UTF-8")
			if tc.wantSuffix != "" {
				assert.True(t, strings.HasSuffix(got, tc.wantSuffix), "expected suffix %q", tc.wantSuffix)
			}
		})
	}
}
