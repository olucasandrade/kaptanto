package vector_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashCache_HitMiss(t *testing.T) {
	dir := t.TempDir()
	cache := vector.OpenHashCache(dir)
	require.False(t, cache.Disabled())
	defer func() { require.NoError(t, cache.Close()) }()

	id := "public.articles:{\"id\":1}"
	hash := vector.HashText("title: Hello\nbody: World")

	_, ok := cache.Get(id)
	assert.False(t, ok, "cold cache should miss")
	assert.False(t, cache.Unchanged(id, hash))

	require.NoError(t, cache.Put(id, hash))
	stored, ok := cache.Get(id)
	require.True(t, ok)
	assert.Equal(t, hash, stored)
	assert.True(t, cache.Unchanged(id, hash), "hit should skip embed")

	changed := vector.HashText("title: Hello\nbody: Changed")
	assert.False(t, cache.Unchanged(id, changed), "different hash is a miss")

	require.NoError(t, cache.Del(id))
	_, ok = cache.Get(id)
	assert.False(t, ok)
}

func TestHashCache_PersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	c1 := vector.OpenHashCache(dir)
	require.False(t, c1.Disabled())
	hash := vector.HashText("persist-me")
	require.NoError(t, c1.Put("k1", hash))
	require.NoError(t, c1.Close())

	c2 := vector.OpenHashCache(dir)
	require.False(t, c2.Disabled())
	defer func() { require.NoError(t, c2.Close()) }()
	assert.True(t, c2.Unchanged("k1", hash))
}

func TestHashCache_DisabledOnCorruptDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vector-hashes.db")
	require.NoError(t, os.WriteFile(path, []byte("this is not a sqlite database"), 0o644))

	cache := vector.OpenHashCache(dir)
	assert.True(t, cache.Disabled(), "corrupt DB must disable cache (VEC-01)")

	hash := vector.HashText("x")
	assert.False(t, cache.Unchanged("id", hash))
	require.NoError(t, cache.Put("id", hash)) // no-op
	require.NoError(t, cache.Del("id"))       // no-op
	require.NoError(t, cache.Close())
}

func TestHashCache_DisabledOnUnopenablePath(t *testing.T) {
	// Point dataDir at a regular file so Join(..., "vector-hashes.db") cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	cache := vector.OpenHashCache(blocker)
	assert.True(t, cache.Disabled())
	require.NoError(t, cache.Close())
}

func TestHashText_Deterministic(t *testing.T) {
	a := vector.HashText("same")
	b := vector.HashText("same")
	c := vector.HashText("different")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Len(t, a, 32)
}

func BenchmarkExtractHash(b *testing.B) {
	ext, err := vector.NewExtractor(config.VectorSourceConfig{
		Columns: []string{"title", "body", "summary"},
	})
	if err != nil {
		b.Fatal(err)
	}
	ev := sampleEvent(map[string]any{
		"title":   "Benchmark Title",
		"body":    "A reasonably long body of text used to exercise extraction and hashing on the Deliver hot path.",
		"summary": "Short summary.",
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		text, err := ext.Extract(ev)
		if err != nil {
			b.Fatal(err)
		}
		_ = vector.HashText(text)
	}
}
