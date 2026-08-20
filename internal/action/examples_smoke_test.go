package action

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/enrich"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/observability"
	vectorsink "github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/require"
)

// exampleYAMLPaths returns every grouped example config
// (examples/{demos,ai,integrations,supporting}/*/kaptanto.yaml).
func exampleYAMLPaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("../../examples/*/*/kaptanto.yaml")
	require.NoError(t, err)
	return matches
}

// TestExamplesConfigsLoad ensures every example kaptanto.yaml can be loaded
// and, when it declares actions, produces valid action consumers. This catches
// drift between example YAMLs and the action type registry.
func TestExamplesConfigsLoad(t *testing.T) {
	matches := exampleYAMLPaths(t)
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
			if name == "lambda" {
				t.Setenv("LAMBDA_FUNCTION_URL", "https://example.lambda-url.us-east-1.on.aws/")
				t.Setenv("AWS_REGION", "us-east-1")
				// SigV4 needs resolvable credentials at webhook sink construction.
				t.Setenv("AWS_ACCESS_KEY_ID", "AKIATESTEXAMPLE")
				t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")
			}
			if name == "mcp-agent" {
				t.Setenv("MCP_API_KEY", "smoke-mcp-key")
			}
			if name == "rag-pgvector" {
				t.Setenv("VECTOR_DSN", "postgres://postgres:postgres@127.0.0.1:5432/rag?sslmode=disable")
			}

			cfg, err := config.Load(path)
			require.NoError(t, err, "load %s", path)

			validateExampleConfig(t, name, cfg)
		})
	}
}

func validateExampleConfig(t *testing.T, name string, cfg *config.Config) {
	t.Helper()
	dataDir := t.TempDir()
	metrics := observability.NewKaptantoMetrics()

	if cfg.MCP.Enabled {
		_, _, err := mcp.ResolveConfig(cfg.MCP, dataDir)
		require.NoError(t, err, "mcp resolve %s", name)
	}

	if strings.TrimSpace(cfg.Enrichment.URL) != "" {
		_, err := enrich.Compile(cfg.Enrichment, metrics)
		require.NoError(t, err, "enrich compile %s", name)
	}

	if cfg.Output == "vector" {
		require.NotNil(t, cfg.Sinks.Vector, "output vector requires sinks.vector (%s)", name)
		vecCfg := *cfg.Sinks.Vector
		require.NoError(t, vectorsink.Validate(&vecCfg), "vector validate %s", name)
	}

	if len(cfg.Actions) > 0 {
		_, err := BuildConsumers(cfg, metrics)
		require.NoError(t, err, "build consumers for %s", name)
	}
}

// TestExamplesNoLiteralSecrets is a lightweight lint: every action param that
// the registry marks as secret must be referenced as ${VAR} in the example
// YAMLs. The registry itself enforces this at runtime; this test gives a
// file-level error message when an example drifts.
func TestExamplesNoLiteralSecrets(t *testing.T) {
	matches := exampleYAMLPaths(t)

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
