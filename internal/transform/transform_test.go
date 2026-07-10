package transform

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileUnknownLanguage(t *testing.T) {
	t.Parallel()

	_, err := Compile("python", "{{.Table}}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown transform language")
	require.Contains(t, err.Error(), "go-template")
	require.Contains(t, err.Error(), "jq")
}

func TestCompileJQNotRegistered(t *testing.T) {
	t.Parallel()

	_, err := Compile(LangJQ, ".table")
	require.Error(t, err)
	require.Contains(t, err.Error(), "jq engine not registered")
	require.Contains(t, err.Error(), "go-template")
	require.Contains(t, err.Error(), "jq")
}

func TestCompileGoTemplateSuccess(t *testing.T) {
	t.Parallel()

	eng, err := Compile(LangGoTemplate, "{{.Table}}")
	require.NoError(t, err)
	require.NotNil(t, eng)
	require.Equal(t, LangGoTemplate, eng.Language())
}

func TestRuntimeError(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	err := &RuntimeError{Language: LangJQ, Cause: cause}
	require.Equal(t, "transform (jq): boom", err.Error())
	require.Equal(t, cause, errors.Unwrap(err))
	require.Equal(t, cause, err.Unwrap())

	// Marker method must exist for router poison classification.
	err.PermanentDelivery()

	var nilErr *RuntimeError
	require.Equal(t, "transform: <nil>", nilErr.Error())
	require.Nil(t, nilErr.Unwrap())

	empty := &RuntimeError{Language: LangGoTemplate}
	require.True(t, strings.HasPrefix(empty.Error(), "transform (go-template):"))
}

func TestAllowlistNamesSorted(t *testing.T) {
	t.Parallel()
	require.Equal(t, "go-template, jq", allowlistNames())
}
