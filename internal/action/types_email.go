package action

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template/parse"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// subjectFieldMap maps Go struct field names used in subject-template
// (e.g. {{.Operation}}) to the corresponding jq JSON paths.
var subjectFieldMap = map[string]string{
	"Operation": ".operation",
	"Table":     ".table",
	"Schema":    ".schema",
	"Database":  ".database",
	"Source":    ".source",
	"ID":       ".id",
}

// templateFieldRe matches {{.FieldName}} patterns in subject templates,
// allowing optional whitespace inside the delimiters.
var templateFieldRe = regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`)

const defaultSubjectTemplate = "[kaptanto] {{.Operation}} on {{.Table}}"

// EmailType is the built-in "email" action type. It sends CDC event
// notifications via the SendGrid v3 HTTP API (ACT-01: data only, no
// delivery code). SMTP is not supported in v1.
type EmailType struct{}

func (EmailType) Name() string { return "email" }

func (EmailType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"provider":         {Required: false, Secret: false, Description: `email provider; must be "sendgrid" or empty (default "sendgrid")`},
		"api-key":          {Required: true, Secret: true, Description: "SendGrid API key"},
		"from":             {Required: true, Secret: false, Description: "sender email address"},
		"to":               {Required: true, Secret: false, Description: "recipient email address"},
		"subject-template": {Required: false, Secret: false, Description: "Go template for email subject", Default: defaultSubjectTemplate},
	}
}

func (EmailType) PinsBatch() bool              { return true }
func (EmailType) ComputedAuthHeaders() []string { return []string{"Authorization"} }

func (EmailType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	provider := p["provider"]
	if provider != "" && provider != "sendgrid" {
		if strings.EqualFold(provider, "smtp") {
			return config.WebhookSinkConfig{}, config.TransformConfig{},
				fmt.Errorf("email: provider %q is not supported; only \"sendgrid\" (HTTP API v3) is available in v1", provider)
		}
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("email: unsupported provider %q; only \"sendgrid\" is supported", provider)
	}

	apiKey := p["api-key"]
	from := p["from"]
	to := p["to"]
	subjectTmpl := p["subject-template"]

	subjectJQ, err := subjectTemplateToJQ(subjectTmpl)
	if err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("email: subject-template: %w", err)
	}

	jqExpr := fmt.Sprintf(
		`{personalizations: [{to: [{email: %s}]}], from: {email: %s}, subject: %s, content: [{type: "text/plain", value: tojson}]}`,
		jqStringLiteral(to), jqStringLiteral(from), subjectJQ,
	)

	whCfg := config.WebhookSinkConfig{
		URL:    "https://api.sendgrid.com/v3/mail/send",
		Method: "POST",
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + apiKey,
		},
		Batch: config.WebhookBatch{MaxEvents: 1},
	}

	transformCfg := config.TransformConfig{
		Language:   "jq",
		Expression: jqExpr,
	}

	return whCfg, transformCfg, nil
}

// subjectTemplateToJQ converts a Go template subject string like
// "[kaptanto] {{.Operation}} on {{.Table}}" into a jq string expression
// like `("[kaptanto] " + .operation + " on " + .table)`.
func subjectTemplateToJQ(tmpl string) (string, error) {
	if err := validateSubjectTemplate(tmpl); err != nil {
		return "", err
	}

	matches := templateFieldRe.FindAllStringSubmatchIndex(tmpl, -1)
	if len(matches) == 0 {
		return jqStringLiteral(tmpl), nil
	}

	var parts []string
	lastEnd := 0

	for _, loc := range matches {
		if loc[0] > lastEnd {
			parts = append(parts, jqStringLiteral(tmpl[lastEnd:loc[0]]))
		}

		fieldName := tmpl[loc[2]:loc[3]]
		jqField, ok := subjectFieldMap[fieldName]
		if !ok {
			return "", fmt.Errorf(
				"unsupported field %q; supported: Operation, Table, Schema, Database, Source",
				fieldName)
		}
		parts = append(parts, jqField)
		lastEnd = loc[1]
	}

	if lastEnd < len(tmpl) {
		parts = append(parts, jqStringLiteral(tmpl[lastEnd:]))
	}

	return "(" + strings.Join(parts, " + ") + ")", nil
}

// jqStringLiteral returns a JSON-encoded string that is also a valid jq string
// literal, escaping the full set of control characters.
func jqStringLiteral(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// validateSubjectTemplate parses the template and rejects anything other than
// literal text and the exact {{.Field}} placeholders listed in subjectFieldMap.
func validateSubjectTemplate(tmpl string) error {
	tree, err := parse.New("subject").Parse(tmpl, "{{", "}}", map[string]*parse.Tree{})
	if err != nil {
		return fmt.Errorf("invalid subject-template: %w", err)
	}
	return walkSubjectNodes(tree.Root)
}

func walkSubjectNodes(node parse.Node) error {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := walkSubjectNodes(child); err != nil {
				return err
			}
		}
	case *parse.TextNode:
		return nil
	case *parse.ActionNode:
		return walkSubjectNodes(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		if len(n.Cmds) != 1 {
			return fmt.Errorf("unsupported subject-template construct")
		}
		cmd := n.Cmds[0]
		if len(cmd.Args) != 1 {
			return fmt.Errorf("unsupported subject-template construct")
		}
		return walkSubjectNodes(cmd.Args[0])
	case *parse.FieldNode:
		if len(n.Ident) != 1 {
			return fmt.Errorf("unsupported subject-template field %q", strings.Join(n.Ident, "."))
		}
		if _, ok := subjectFieldMap[n.Ident[0]]; !ok {
			return fmt.Errorf("unsupported field %q", n.Ident[0])
		}
		return nil
	default:
		return fmt.Errorf("unsupported subject-template construct %T", node)
	}
	return nil
}

func init() { DefaultRegistry.Register(EmailType{}) }
