package transform

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// goTemplateEngine executes a text/template against *event.ChangeEvent.
type goTemplateEngine struct {
	tmpl *template.Template
}

// compileGoTemplate parses expression with text/template (TRF-01).
func compileGoTemplate(expression string) (Engine, error) {
	tmpl, err := template.New("transform").Option("missingkey=error").Parse(expression)
	if err != nil {
		return nil, fmt.Errorf("go-template parse: %w", err)
	}
	return &goTemplateEngine{tmpl: tmpl}, nil
}

func (e *goTemplateEngine) Language() string {
	return LangGoTemplate
}

// Apply executes the template against ev. Empty TrimSpace output drops (TRF-02).
// Execution errors are wrapped as *RuntimeError. raw is unused (jq path).
func (e *goTemplateEngine) Apply(_ []byte, ev *event.ChangeEvent) ([]byte, bool, error) {
	var buf bytes.Buffer
	if err := e.tmpl.Execute(&buf, ev); err != nil {
		return nil, false, &RuntimeError{Language: LangGoTemplate, Cause: err}
	}
	out := buf.Bytes()
	if strings.TrimSpace(string(out)) == "" {
		return nil, true, nil
	}
	return out, false, nil
}
