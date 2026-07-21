package vector_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type permanentMarker interface {
	PermanentDelivery()
}

func sampleEvent(after map[string]any) *event.ChangeEvent {
	var raw json.RawMessage
	if after != nil {
		b, err := json.Marshal(after)
		if err != nil {
			panic(err)
		}
		raw = b
	}
	return &event.ChangeEvent{
		ID:        ulid.MustNew(ulid.Timestamp(time.Unix(1_700_000_000, 0)), nil),
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
		Source:    "pg",
		Operation: event.OpUpdate,
		Schema:    "public",
		Table:     "articles",
		Key:       json.RawMessage(`{"id":1}`),
		After:     raw,
		Metadata:  map[string]any{},
	}
}

func TestExtract_ColumnsGolden(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title", "body", "views"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{
		"title": "Hello",
		"body":  "World",
		"views": float64(42),
		"extra": "ignored",
	}))
	require.NoError(t, err)
	assert.Equal(t, "title: Hello\nbody: World\nviews: 42", text)
}

func TestExtract_ColumnsOrderPreserved(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"b", "a"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{"a": "A", "b": "B"}))
	require.NoError(t, err)
	assert.Equal(t, "b: B\na: A", text)
}

func TestExtract_ColumnsMissingValue(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title", "missing"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{"title": "x"}))
	require.NoError(t, err)
	assert.Equal(t, "title: x\nmissing:", text)
}

func TestExtract_ColumnsNestedJSON(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"meta"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{
		"meta": map[string]any{"k": "v"},
	}))
	require.NoError(t, err)
	assert.Equal(t, `meta: {"k":"v"}`, text)
}

func TestExtract_ColumnsEmptyAfterDrops(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(nil))
	require.NoError(t, err)
	assert.Equal(t, "", text)
	// Callers drop empty text and increment vector_skipped_total{reason="empty"}.
	assert.Equal(t, "empty", vector.SkipReasonEmpty)
}

func TestExtract_TemplateGolden(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Template: `{{.Schema}}.{{.Table}} [{{.Operation}}]: {{index .Metadata "note"}}`,
	})
	require.NoError(t, err)

	ev := sampleEvent(map[string]any{"title": "x"})
	ev.Metadata["note"] = "n1"
	text, err := ext.Extract(ev)
	require.NoError(t, err)
	assert.Equal(t, "public.articles [update]: n1", text)
}

func TestExtract_TemplateEmptyDrops(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Template: `{{if false}}x{{end}}`,
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{"title": "x"}))
	require.NoError(t, err)
	assert.Equal(t, "", text)
}

func TestExtract_TemplateRuntimeErrorPermanent(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Template: `{{.MissingField}}`,
	})
	require.NoError(t, err)

	_, err = ext.Extract(sampleEvent(map[string]any{"title": "x"}))
	require.Error(t, err)

	var perm permanentMarker
	require.True(t, errors.As(err, &perm), "expected PermanentDelivery marker, got %T", err)

	var extractErr *vector.ExtractError
	require.True(t, errors.As(err, &extractErr))
	extractErr.PermanentDelivery()
}

func TestExtract_TemplateParseErrorAtNew(t *testing.T) {
	_, err := vector.NewExtractor(config.VectorSourceConfig{
		Template: `{{.Table`,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template parse")
}

func TestExtract_BothModesRejected(t *testing.T) {
	_, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns:  []string{"a"},
		Template: "{{.Table}}",
	})
	require.Error(t, err)
}

func TestExtract_NeitherModeRejected(t *testing.T) {
	_, err := vector.NewExtractor(config.VectorSourceConfig{})
	require.Error(t, err)
}

func TestExtract_MalformedAfterPermanent(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title"},
	})
	require.NoError(t, err)

	ev := sampleEvent(nil)
	ev.After = json.RawMessage(`not-json`)
	_, err = ext.Extract(ev)
	require.Error(t, err)
	var perm permanentMarker
	require.True(t, errors.As(err, &perm))
}
