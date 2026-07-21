package vector_test

import (
	"encoding/json"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalID_SortedKeyFields(t *testing.T) {
	id1, err := vector.CanonicalID("public", "orders", map[string]any{
		"b": "2",
		"a": "1",
	})
	require.NoError(t, err)
	id2, err := vector.CanonicalID("public", "orders", map[string]any{
		"a": "1",
		"b": "2",
	})
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "key field order must not affect ID (VEC-03)")
	assert.Equal(t, `public.orders:{"a":"1","b":"2"}`, id1)
}

func TestCanonicalID_StableAcrossRetries(t *testing.T) {
	key := map[string]any{"id": int32(42), "tenant": "acme"}
	first, err := vector.CanonicalID("public", "orders", key)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		again, err := vector.CanonicalID("public", "orders", key)
		require.NoError(t, err)
		assert.Equal(t, first, again)
	}
	// Native int and string WAL form converge via pk.Canonical.
	wal, err := vector.CanonicalID("public", "orders", map[string]any{"id": "42", "tenant": "acme"})
	require.NoError(t, err)
	assert.Equal(t, first, wal)
}

func TestCanonicalID_EmptySchema(t *testing.T) {
	id, err := vector.CanonicalID("", "docs", map[string]any{"_id": "abc"})
	require.NoError(t, err)
	assert.Equal(t, `docs:{"_id":"abc"}`, id)
}

func TestCanonicalID_RequiresTable(t *testing.T) {
	_, err := vector.CanonicalID("public", "", map[string]any{"id": "1"})
	require.Error(t, err)
}

func TestCanonicalIDFromRaw(t *testing.T) {
	id, err := vector.CanonicalIDFromRaw("public", "orders", json.RawMessage(`{"id":"7"}`))
	require.NoError(t, err)
	assert.Equal(t, `public.orders:{"id":"7"}`, id)

	idNil, err := vector.CanonicalIDFromRaw("public", "orders", nil)
	require.NoError(t, err)
	assert.Equal(t, `public.orders:{}`, idNil)

	_, err = vector.CanonicalIDFromRaw("public", "orders", json.RawMessage(`not-json`))
	require.Error(t, err)
}
