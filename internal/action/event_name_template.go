package action

import (
	"fmt"
	"strings"
	"text/template/parse"
)

// eventNameFieldMap maps Go-template field names used in event-name templates
// to the corresponding jq paths on a ChangeEvent.
var eventNameFieldMap = map[string]string{
	"Table":     ".table",
	"Operation": ".operation",
	"Schema":    ".schema",
}

// validateEventNameTemplate parses a template and rejects any construct other
// than literal text and the field placeholders {{.Table}}, {{.Operation}}, and
// {{.Schema}} (with optional whitespace). This prevents pipelines, conditionals,
// unknown fields, and other Go template features from silently becoming literal
// event names.
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
		if len(n.Ident) != 1 {
			return fmt.Errorf("unsupported field %q", strings.Join(n.Ident, "."))
		}
		if _, ok := eventNameFieldMap[n.Ident[0]]; !ok {
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

// renderEventNameExpr converts a Go template string like
// "kaptanto/{{ .Table }}.{{ .Operation }}" into a jq string expression like
// `("kaptanto/" + .table + "." + .operation)`. It allows optional whitespace
// inside the delimiters and rejects unknown fields.
func renderEventNameExpr(tmpl string) (string, error) {
	matches := templateFieldRe.FindAllStringSubmatchIndex(tmpl, -1)
	if len(matches) == 0 {
		if strings.Contains(tmpl, "{{") || strings.Contains(tmpl, "}}") {
			return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
		}
		return jqStringLiteral(tmpl), nil
	}

	var parts []string
	lastEnd := 0

	for _, loc := range matches {
		if loc[0] > lastEnd {
			literal := tmpl[lastEnd:loc[0]]
			if strings.Contains(literal, "{{") || strings.Contains(literal, "}}") {
				return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
			}
			parts = append(parts, jqStringLiteral(literal))
		}

		fieldName := tmpl[loc[2]:loc[3]]
		jqField, ok := eventNameFieldMap[fieldName]
		if !ok {
			return "", fmt.Errorf("unsupported field %q; supported: Table, Operation, Schema", fieldName)
		}
		parts = append(parts, jqField)
		lastEnd = loc[1]
	}

	if lastEnd < len(tmpl) {
		literal := tmpl[lastEnd:]
		if strings.Contains(literal, "{{") || strings.Contains(literal, "}}") {
			return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
		}
		parts = append(parts, jqStringLiteral(literal))
	}

	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " + ") + ")", nil
}
