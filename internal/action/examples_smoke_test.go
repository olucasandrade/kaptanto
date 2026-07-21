package action

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/stretchr/testify/require"
)

// TestExamplesConfigsLoad ensures every examples/*/kaptanto.yaml can be loaded
// and, when it declares actions, produces valid action consumers. This catches
// drift between example YAMLs and the action type registry.
func TestExamplesConfigsLoad(t *testing.T) {
	matches, err := filepath.Glob("../../examples/*/kaptanto.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no example kaptanto.yaml files found")

	for _, path := range matches {
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			if name == "inngest" {
				t.Setenv("INNGEST_EVENT_KEY", "local")
			}
			if name == "trigger-dev" {
				t.Setenv("TRIGGERDEV_API_KEY", "tr_dev_test_key")
			}

			cfg, err := config.Load(path)
			require.NoError(t, err, "load %s", path)

			if len(cfg.Actions) == 0 {
				return
			}

			_, err = BuildConsumers(cfg, observability.NewKaptantoMetrics())
			require.NoError(t, err, "build consumers for %s", path)
		})
	}
}

// TestExamplesNoLiteralSecrets is a lightweight lint: every action param that
// the registry marks as secret must be referenced as ${VAR} in the example
// YAMLs. The registry itself enforces this at runtime; this test gives a
// file-level error message when an example drifts.
func TestExamplesNoLiteralSecrets(t *testing.T) {
	matches, err := filepath.Glob("../../examples/*/kaptanto.yaml")
	require.NoError(t, err)

	for _, path := range matches {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		// Very coarse guard: secret params use ${...}; literal values in secret
		// params are forbidden. This regex is intentionally simple and only
		// flags obvious literals like api-key: "tr_dev_..." or event-key: "local".
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "api-key:") || strings.HasPrefix(trimmed, "event-key:") || strings.HasPrefix(trimmed, "bearer-token:") || strings.HasPrefix(trimmed, "password:") || strings.HasPrefix(trimmed, "secret:") {
				keyEnd := strings.Index(trimmed, ":")
				val := strings.TrimSpace(trimmed[keyEnd+1:])
				if idx := strings.Index(val, "#"); idx >= 0 {
					val = strings.TrimSpace(val[:idx])
				}
				val = strings.Trim(val, `"'`)
				if val != "" && !strings.HasPrefix(val, "${") {
					t.Errorf("%s:%d: secret param looks literal: %s", path, i+1, trimmed)
				}
			}
		}
	}
}
