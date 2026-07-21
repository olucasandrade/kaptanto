package action

import (
	"strings"
	"testing"
)

func TestURLTemplateToJQ_AcceptsSupportedFields(t *testing.T) {
	cases := []struct {
		name     string
		template string
		wantSub  string
	}{
		{"plain URL", "https://cdn.example.com/path", `"https://cdn.example.com/path"`},
		{"single field", "https://cdn.example.com/{{.Table}}", "(.table | tostring)"},
		{"multiple fields", "https://cdn.example.com/{{.Schema}}/{{.Table}}", "(.schema | tostring)"},
		{"field with surrounding text", "https://cdn.example.com/prefix-{{.Table}}-suffix", "prefix-"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := urlTemplateToJQ(tc.template)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("expected %q to contain %q", got, tc.wantSub)
			}
		})
	}
}

func TestURLTemplateToJQ_RejectsUnsupportedSyntax(t *testing.T) {
	cases := []string{
		`https://cdn.example.com/{{ .Table | upper }}`,
		`https://cdn.example.com/{{if .Table}}x{{end}}`,
		`https://cdn.example.com/{{.UnknownField}}`,
		`https://cdn.example.com/{{.Table`,
		`https://cdn.example.com/.Table}}`,
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			_, err := urlTemplateToJQ(tc)
			if err == nil {
				t.Fatalf("expected error for template %q", tc)
			}
		})
	}
}
