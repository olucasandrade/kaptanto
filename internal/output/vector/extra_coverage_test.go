package vector_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestExtract_NilExtractor(t *testing.T) {
	var ext *vector.Extractor
	_, err := ext.Extract(sampleEvent(map[string]any{"a": 1}))
	require.Error(t, err)
}

func TestExtract_FormatValueBranches(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"flag", "ratio", "list"},
	})
	require.NoError(t, err)

	text, err := ext.Extract(sampleEvent(map[string]any{
		"flag":  true,
		"ratio": 1.5,
		"list":  []any{"a", float64(1)},
	}))
	require.NoError(t, err)
	assert.Equal(t, "flag: true\nratio: 1.5\nlist: [\"a\",1]", text)
}

func TestFormatValue_JSONNumberAndNil(t *testing.T) {
	assert.Equal(t, "", vector.FormatValueForTest(nil))
	assert.Equal(t, "99", vector.FormatValueForTest(json.Number("99")))
	assert.Equal(t, "hello", vector.FormatValueForTest("hello"))
}

func TestExtractError_Methods(t *testing.T) {
	var nilErr *vector.ExtractError
	assert.Equal(t, "vector extract: <nil>", nilErr.Error())
	assert.Nil(t, nilErr.Unwrap())
	nilErr.PermanentDelivery()

	empty := &vector.ExtractError{}
	assert.Equal(t, "vector extract: <nil cause>", empty.Error())
	assert.Nil(t, empty.Unwrap())
	empty.PermanentDelivery()

	wrapped := &vector.ExtractError{Cause: errors.New("boom")}
	assert.Equal(t, "vector extract: boom", wrapped.Error())
	assert.EqualError(t, wrapped.Unwrap(), "boom")
	wrapped.PermanentDelivery()
}

func TestHashCache_NilReceiver(t *testing.T) {
	var c *vector.HashCache
	assert.True(t, c.Disabled())
	_, ok := c.Get("x")
	assert.False(t, ok)
	assert.False(t, c.Unchanged("x", vector.HashText("y")))
	require.NoError(t, c.Put("x", vector.HashText("y")))
	require.NoError(t, c.Del("x"))
	require.NoError(t, c.Close())
}

func TestHashCache_UnchangedLengthMismatch(t *testing.T) {
	dir := t.TempDir()
	cache := vector.OpenHashCache(dir)
	require.False(t, cache.Disabled())
	defer func() { require.NoError(t, cache.Close()) }()

	require.NoError(t, cache.Put("id", []byte{1, 2, 3}))
	assert.False(t, cache.Unchanged("id", []byte{1, 2}))
	assert.False(t, cache.Unchanged("id", []byte{9, 2, 3}))
}

func TestHashCache_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	cache := vector.OpenHashCache(dir)
	require.False(t, cache.Disabled())
	require.NoError(t, cache.Close())
	require.NoError(t, cache.Close())
	assert.True(t, cache.Disabled())
}

func TestHashCache_GetAfterCloseIsMiss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vector-hashes.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE hashes (id TEXT PRIMARY KEY, hash BLOB NOT NULL, updated_at INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO hashes (id, hash, updated_at) VALUES ('k', ?, 1)`, vector.HashText("t"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	cache := vector.OpenHashCache(dir)
	require.False(t, cache.Disabled())
	require.NoError(t, cache.Close())
	_, ok := cache.Get("k")
	assert.False(t, ok)
}

func TestValidate_BatchPreservedWhenSet(t *testing.T) {
	cfg := validBase()
	cfg.Batch.MaxEvents = 12
	require.NoError(t, vector.Validate(&cfg))
	assert.Equal(t, 12, cfg.Batch.MaxEvents)
}

func TestValidate_WhitespaceSecretRejected(t *testing.T) {
	cfg := validBase()
	cfg.Embedder.APIKey = "  sk-x  "
	err := vector.Validate(&cfg)
	require.Error(t, err)
}

func TestExtract_ColumnsBoolFalse(t *testing.T) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{Columns: []string{"ok"}})
	require.NoError(t, err)
	text, err := ext.Extract(sampleEvent(map[string]any{"ok": false}))
	require.NoError(t, err)
	assert.Equal(t, "ok: false", text)
}
