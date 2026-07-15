package action

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// inngestType implements the "inngest" action type (ACT-01).
//
// Inngest accepts events via POST https://inn.gs/e/<event-key>.
// The event-key is embedded in the URL path (not a header), so there are no
// computed auth headers. Inngest accepts JSON arrays, so batching is allowed.
type inngestType struct{}

func init() { DefaultRegistry.Register(&inngestType{}) }

func (*inngestType) Name() string { return "inngest" }

func (*inngestType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"event-key": {
			Required:    true,
			Secret:      true,
			Description: "Inngest event key (embedded in URL path)",
		},
		"event-name-template": {
			Required:    false,
			Secret:      false,
			Description: "Go template for the Inngest event name field",
			Default:     "kaptanto/{{.Table}}.{{.Operation}}",
		},
	}
}

func (*inngestType) PinsBatch() bool          { return false }
func (*inngestType) ComputedAuthHeaders() []string { return nil }

func (*inngestType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	eventKey := p["event-key"]
	nameTemplate := p["event-name-template"]

	if _, err := template.New("").Parse(nameTemplate); err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("inngest: invalid event-name-template: %w", err)
	}

	nameExpr := renderNameExpr(nameTemplate)

	url := "https://inn.gs/e/" + eventKey

	whCfg := config.WebhookSinkConfig{
		URL:    url,
		Method: "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}

	jqExpr := fmt.Sprintf(
		`{name: %s, id: .idempotency_key, ts: (.ts // now*1000 | floor), data: .}`,
		nameExpr,
	)

	transform := config.TransformConfig{
		Language:   "jq",
		Expression: jqExpr,
	}

	return whCfg, transform, nil
}

// renderNameExpr converts a Go template string like "kaptanto/{{.Table}}.{{.Operation}}"
// into a jq string expression like ("kaptanto/" + .table + "." + .operation).
// The CDC event JSON uses lowercase field names (.table, .operation, .schema).
func renderNameExpr(tmpl string) string {
	fieldMap := map[string]string{
		"{{.Table}}":     ".table",
		"{{.Operation}}": ".operation",
		"{{.Schema}}":    ".schema",
	}

	parts := []string{}
	remaining := tmpl
	for len(remaining) > 0 {
		earliest := -1
		var earliestKey, earliestField string
		for key, field := range fieldMap {
			idx := strings.Index(remaining, key)
			if idx >= 0 && (earliest < 0 || idx < earliest) {
				earliest = idx
				earliestKey = key
				earliestField = field
			}
		}
		if earliest < 0 {
			parts = append(parts, fmt.Sprintf("%q", remaining))
			break
		}
		if earliest > 0 {
			parts = append(parts, fmt.Sprintf("%q", remaining[:earliest]))
		}
		parts = append(parts, earliestField)
		remaining = remaining[earliest+len(earliestKey):]
	}

	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " + ") + ")"
}
