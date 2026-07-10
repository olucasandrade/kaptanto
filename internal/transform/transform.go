// Package transform compiles and applies per-event payload transforms.
//
// Invariants:
//
//   - TRF-01 — Fail-fast compile: transform expressions compile at startup;
//     a parse error aborts startup (Compile returns error).
//   - TRF-02 — Drop advances: a drop (null/empty result) means the caller
//     acks the event — cursor advances, nothing is sent.
package transform

import (
	"fmt"
	"sort"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// Supported transform languages. Only registered engines can be compiled.
const (
	LangGoTemplate = "go-template"
	LangJQ         = "jq"
)

// Engine applies a compiled expression to one ChangeEvent.
// raw is the event's canonical JSON (eventlog entry.Raw fast path or a
// json.Marshal of ev); ev is the decoded struct. Engines use whichever
// representation is natural: jq uses raw, go-template uses ev.
//
// Returns (out, false, nil) to deliver out as the payload;
// (nil, true, nil) to drop the event (TRF-02);
// (nil, false, err) on failure — err is always a *RuntimeError.
type Engine interface {
	Apply(raw []byte, ev *event.ChangeEvent) (out []byte, drop bool, err error)
	Language() string
}

// compilerFunc parses an expression into an Engine.
type compilerFunc func(expression string) (Engine, error)

// compilers maps language name → compile function.
var compilers = map[string]compilerFunc{
	LangGoTemplate: compileGoTemplate,
	LangJQ:         compileJQ,
}

// allowlist is the set of languages Compile accepts by name. Unknown
// language errors always name this list.
var allowlist = []string{LangGoTemplate, LangJQ}

// Compile parses expression for the given language ("jq" | "go-template").
// Unknown language or parse failure => error (TRF-01, startup-fatal for callers).
func Compile(language, expression string) (Engine, error) {
	fn, ok := compilers[language]
	if !ok {
		return nil, unsupportedLanguageError(language)
	}
	return fn(expression)
}

func unsupportedLanguageError(language string) error {
	return fmt.Errorf("unknown transform language %q; supported languages: %s", language, allowlistNames())
}

func allowlistNames() string {
	sorted := append([]string(nil), allowlist...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// RuntimeError marks a deterministic per-event transform failure. It
// implements PermanentDelivery() so the router classifies it as poison.
type RuntimeError struct {
	Language string
	Cause    error
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return "transform: <nil>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("transform (%s): <nil>", e.Language)
	}
	return fmt.Sprintf("transform (%s): %v", e.Language, e.Cause)
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PermanentDelivery marks RuntimeError as a non-retryable delivery failure.
func (e *RuntimeError) PermanentDelivery() {}
