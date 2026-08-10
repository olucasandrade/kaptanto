// White-box tests for the unexported buildTLSConfig helper.
package rabbitmqsink

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
)

func TestBuildTLSConfig_MinVersion(t *testing.T) {
	cfg, err := buildTLSConfig(config.TLSConfig{})
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}
