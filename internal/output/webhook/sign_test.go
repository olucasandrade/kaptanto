package webhooksink

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSignature_KnownDigest(t *testing.T) {
	secret := []byte("whsec_test")
	body := []byte(`{"id":"1"}`)
	got := signature(secret, 1609459200, body)
	assert.Equal(t, "t=1609459200,v1=b49de3f45faf13a4801fd5a143c4d7841f8b253714027869eafbf82966520a35", got)
}

func TestSignature_EmptySecret(t *testing.T) {
	assert.Equal(t, "", signature(nil, 1609459200, []byte(`{"id":"1"}`)))
	assert.Equal(t, "", signature([]byte{}, 1609459200, []byte(`{"id":"1"}`)))
}
