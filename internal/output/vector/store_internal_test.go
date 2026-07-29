package vector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatVector(t *testing.T) {
	assert.Equal(t, "[]", formatVector(nil))
	assert.Equal(t, "[]", formatVector([]float32{}))
	assert.Equal(t, "[0.1,0.2,0.3]", formatVector([]float32{0.1, 0.2, 0.3}))
}

func TestValidateIdent(t *testing.T) {
	require.NoError(t, validateIdent("kaptanto_vectors"))
	require.NoError(t, validateIdent("_x"))
	require.Error(t, validateIdent(""))
	require.Error(t, validateIdent("a-b"))
	require.Error(t, validateIdent("1abc"))
	require.Error(t, validateIdent(`evil"; drop`))
}

func TestOpenPGVector_Validation(t *testing.T) {
	_, err := OpenPGVector(t.Context(), "", "t", 3)
	require.Error(t, err)
	_, err = OpenPGVector(t.Context(), "postgres://x", "t", 0)
	require.Error(t, err)
	_, err = OpenPGVector(t.Context(), "postgres://x", "bad-name", 3)
	require.Error(t, err)
}

func TestQdrantPointID_Deterministic(t *testing.T) {
	a := qdrantPointID(`public.orders:{"id":"1"}`)
	b := qdrantPointID(`public.orders:{"id":"1"}`)
	c := qdrantPointID(`public.orders:{"id":"2"}`)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, a)
}

func TestMarshalMetadata(t *testing.T) {
	b, err := marshalMetadata(nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("{}"), b)
	b, err = marshalMetadata(map[string]any{"a": 1})
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(b))
}
