package auth_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/stretchr/testify/assert"
)

func TestConsumerScopeID_StableAndNonEmpty(t *testing.T) {
	id1 := auth.ConsumerScopeID("secret-token")
	id2 := auth.ConsumerScopeID("secret-token")
	assert.Equal(t, id1, id2)
	assert.NotEmpty(t, id1)
	assert.NotEqual(t, auth.ConsumerScopeID("other-token"), id1)
	assert.Empty(t, auth.ConsumerScopeID(""))
}
