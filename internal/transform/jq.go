package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
	"github.com/olucasandrade/kaptanto/internal/event"
)

// defaultJQRuntimeTimeout bounds how long a single jq filter may execute.
// A context with this timeout is passed to RunWithContext so recursive or
// non-emitting programs cannot block the router indefinitely.
const defaultJQRuntimeTimeout = 30 * time.Second

// jqRuntimeTimeout is the mutable runtime bound so tests can exercise
// cancellation without waiting 30s.
var jqRuntimeTimeout = defaultJQRuntimeTimeout

// jqEngine evaluates a compiled gojq expression against raw JSON.
type jqEngine struct {
	code *gojq.Code
}

// compileJQ parses and compiles expression with gojq (TRF-01).
// Undefined variables/functions fail at Compile, not Apply.
func compileJQ(expression string) (Engine, error) {
	query, err := gojq.Parse(expression)
	if err != nil {
		return nil, fmt.Errorf("jq parse: %w", err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("jq compile: %w", err)
	}
	return &jqEngine{code: code}, nil
}

func (e *jqEngine) Language() string {
	return LangJQ
}

// Apply unmarshals raw JSON, runs the compiled jq expression, and enforces
// single-output semantics:
//
//   - 0 outputs, or exactly one nil (JSON null) → drop (TRF-02)
//   - exactly one non-nil value → json.Marshal → out
//   - ≥2 outputs → *RuntimeError
//   - gojq runtime error → *RuntimeError
//
// Iteration stops after the second output so expressions like repeat(1)
// cannot hang. ev is unused (go-template path).
func (e *jqEngine) Apply(raw []byte, _ *event.ChangeEvent) ([]byte, bool, error) {
	var input any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		return nil, false, &RuntimeError{
			Language: LangJQ,
			Cause:    fmt.Errorf("json unmarshal: %w", err),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), jqRuntimeTimeout)
	defer cancel()

	iter := e.code.RunWithContext(ctx, input)
	var (
		first     any
		haveFirst bool
		count     int
	)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return nil, false, &RuntimeError{Language: LangJQ, Cause: err}
		}
		count++
		if count == 1 {
			first = v
			haveFirst = true
			continue
		}
		// Iteration guard: stop after the 2nd output (repeat(1) safety).
		return nil, false, &RuntimeError{
			Language: LangJQ,
			Cause:    fmt.Errorf("expression produced %d outputs; must produce exactly one", count),
		}
	}

	if count == 0 {
		return nil, true, nil
	}
	if !haveFirst || first == nil {
		return nil, true, nil
	}

	out, err := json.Marshal(first)
	if err != nil {
		return nil, false, &RuntimeError{
			Language: LangJQ,
			Cause:    fmt.Errorf("json marshal: %w", err),
		}
	}
	return out, false, nil
}
