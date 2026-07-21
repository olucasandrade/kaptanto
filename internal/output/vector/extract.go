package vector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
)

// SkipReasonEmpty is the metric label reason when extracted text is empty.
// Callers that drop on empty text should increment
// vector_skipped_total{reason="empty"}.
const SkipReasonEmpty = "empty"

// Extractor turns a ChangeEvent into embeddable text using either a column
// list or a compiled go-template (compiled at NewExtractor / Validate).
type Extractor struct {
	columns []string
	tmpl    *template.Template
}

// NewExtractor builds an Extractor from a validated source config.
// Template parse errors are returned (startup-fatal). Prefer Validate first.
func NewExtractor(src config.VectorSourceConfig) (*Extractor, error) {
	hasCols := len(src.Columns) > 0
	hasTmpl := strings.TrimSpace(src.Template) != ""
	switch {
	case hasCols && hasTmpl:
		return nil, fmt.Errorf("vector: source: exactly one of columns or template must be set, got both")
	case !hasCols && !hasTmpl:
		return nil, fmt.Errorf("vector: source: exactly one of columns or template must be set, got neither")
	case hasTmpl:
		tmpl, err := template.New("vector-source").Option("missingkey=error").Parse(src.Template)
		if err != nil {
			return nil, fmt.Errorf("vector: source: template parse: %w", err)
		}
		return &Extractor{tmpl: tmpl}, nil
	default:
		cols := make([]string, len(src.Columns))
		copy(cols, src.Columns)
		return &Extractor{columns: cols}, nil
	}
}

// Extract returns the text to embed.
//
// An empty text with a nil error means the event should be dropped (acked)
// with skip reason SkipReasonEmpty. Template execution failures return an
// *ExtractError that implements PermanentDelivery() for immediate DLQ.
func (e *Extractor) Extract(ev *event.ChangeEvent) (text string, err error) {
	if e == nil {
		return "", fmt.Errorf("vector: extractor is nil")
	}
	if e.tmpl != nil {
		return e.extractTemplate(ev)
	}
	return e.extractColumns(ev)
}

func (e *Extractor) extractColumns(ev *event.ChangeEvent) (string, error) {
	if ev == nil || len(ev.After) == 0 {
		return "", nil
	}
	var after map[string]any
	if err := json.Unmarshal(ev.After, &after); err != nil {
		return "", &ExtractError{Cause: fmt.Errorf("after json: %w", err)}
	}

	var b strings.Builder
	for i, col := range e.columns {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(col)
		b.WriteString(": ")
		b.WriteString(formatValue(after[col]))
	}
	out := strings.TrimSpace(b.String())
	return out, nil
}

func (e *Extractor) extractTemplate(ev *event.ChangeEvent) (string, error) {
	var buf bytes.Buffer
	if err := e.tmpl.Execute(&buf, ev); err != nil {
		return "", &ExtractError{Cause: err}
	}
	return strings.TrimSpace(buf.String()), nil
}

func formatValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// ExtractError is a deterministic per-event extraction failure (typically a
// template runtime error). It implements PermanentDelivery() so the router
// classifies it as poison (immediate DLQ).
type ExtractError struct {
	Cause error
}

func (e *ExtractError) Error() string {
	if e == nil {
		return "vector extract: <nil>"
	}
	if e.Cause == nil {
		return "vector extract: <nil cause>"
	}
	return "vector extract: " + e.Cause.Error()
}

func (e *ExtractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PermanentDelivery marks ExtractError as a non-retryable delivery failure.
func (e *ExtractError) PermanentDelivery() {}
