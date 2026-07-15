package action

import (
	"fmt"
	"strings"
	"text/template/parse"

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

	if err := validateEventNameTemplate(nameTemplate); err != nil {
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

var allowedEventNameFields = map[string]bool{
	"Table":     true,
	"Operation": true,
	"Schema":    true,
}

// validateEventNameTemplate parses the template and rejects any construct other
// than literal text and the exact field placeholders {{.Table}}, {{.Operation}},
// and {{.Schema}}. This prevents pipelines, conditionals, and other Go template
// features from silently becoming literal event names.
func validateEventNameTemplate(tmpl string) error {
	tree, err := parse.New("event-name").Parse(tmpl, "{{", "}}", map[string]*parse.Tree{})
	if err != nil {
		return err
	}
	return walkEventNameNodes(tree.Root)
}

func walkEventNameNodes(node parse.Node) error {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := walkEventNameNodes(child); err != nil {
				return err
			}
		}
	case *parse.TextNode:
		return nil
	case *parse.ActionNode:
		return walkEventNameNodes(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		// A valid placeholder is a single command with one field argument.
		if len(n.Cmds) != 1 {
			return fmt.Errorf("unsupported template construct")
		}
		cmd := n.Cmds[0]
		if len(cmd.Args) != 1 {
			return fmt.Errorf("unsupported template construct")
		}
		return walkEventNameNodes(cmd.Args[0])
	case *parse.FieldNode:
		if len(n.Ident) != 1 || !allowedEventNameFields[n.Ident[0]] {
			return fmt.Errorf("unsupported field %q", strings.Join(n.Ident, "."))
		}
		return nil
	case *parse.StringNode:
		return nil
	default:
		return fmt.Errorf("unsupported template construct %T", node)
	}
	return nil
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
