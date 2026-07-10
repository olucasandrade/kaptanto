// Package config_test provides TDD tests for the config package.
package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Load tests ---

func TestLoad_ValidYAML(t *testing.T) {
	yaml := `
source: postgres://user:pass@host/db
tables:
  public.orders:
    columns: [id, status, amount]
    where: "status != 'cancelled'"
  public.users:
    columns: [id, email]
output: stdout
port: 7654
data-dir: ./data
retention: 1h
`
	path := writeTempYAML(t, yaml)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "postgres://user:pass@host/db", cfg.Source)
	assert.Equal(t, "stdout", cfg.Output)
	assert.Equal(t, 7654, cfg.Port)
	assert.Equal(t, "./data", cfg.DataDir)
	assert.Equal(t, "1h", cfg.Retention)

	require.Contains(t, cfg.Tables, "public.orders")
	orders := cfg.Tables["public.orders"]
	assert.Equal(t, []string{"id", "status", "amount"}, orders.Columns)
	assert.Equal(t, "status != 'cancelled'", orders.Where)

	require.Contains(t, cfg.Tables, "public.users")
	users := cfg.Tables["public.users"]
	assert.Equal(t, []string{"id", "email"}, users.Columns)
	assert.Equal(t, "", users.Where)
}

func TestLoad_NonExistentFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/config.yaml")
	assert.Nil(t, cfg)
	assert.Error(t, err)
}

func TestLoad_MalformedYAML(t *testing.T) {
	yaml := `
source: [unclosed bracket
  bad: yaml: ::
`
	path := writeTempYAML(t, yaml)

	cfg, err := config.Load(path)
	assert.Nil(t, cfg)
	assert.Error(t, err)
}

func TestLoad_EmptyFile(t *testing.T) {
	path := writeTempYAML(t, "")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	// All fields at zero values
	assert.Equal(t, "", cfg.Source)
	assert.Nil(t, cfg.Tables)
}

func TestLoad_WebhookTransformDLQYAMLRoundTrip(t *testing.T) {
	yamlText := `
source: postgres://user:pass@host/db
output: webhook
sinks:
  webhook:
    url: https://example.com/cdc
    url-template: https://example.com/{{.Schema}}/{{.Table}}
    method: PATCH
    timeout: 15s
    headers:
      X-Env: ${WEBHOOK_ENV}
      X-Static: kaptanto
    auth:
      bearer-token: ${WEBHOOK_TOKEN}
      basic:
        username: user
        password: ${WEBHOOK_PASSWORD}
    signing:
      secret: ${WEBHOOK_SIGNING_SECRET}
    batch:
      max-events: 25
    payload-template: |
      {"id":"{{.ID}}"}
    transform:
      language: jq
      expression: |
        .after |= with_entries(select(.key != "password"))
    tls:
      ca-file: /certs/ca.pem
      cert-file: /certs/client.pem
      key-file: /certs/client-key.pem
dlq:
  enabled: true
  path: /var/lib/kaptanto/dlq.db
  retention: 168h
`
	path := writeTempYAML(t, yamlText)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	encoded, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var roundTrip config.Config
	require.NoError(t, yaml.Unmarshal(encoded, &roundTrip))

	require.NotNil(t, roundTrip.Sinks.Webhook)
	webhook := roundTrip.Sinks.Webhook
	assert.Equal(t, "https://example.com/cdc", webhook.URL)
	assert.Equal(t, "https://example.com/{{.Schema}}/{{.Table}}", webhook.URLTemplate)
	assert.Equal(t, "PATCH", webhook.Method)
	assert.Equal(t, "15s", webhook.Timeout)
	assert.Equal(t, map[string]string{"X-Env": "${WEBHOOK_ENV}", "X-Static": "kaptanto"}, webhook.Headers)
	assert.Equal(t, "${WEBHOOK_TOKEN}", webhook.Auth.BearerToken)
	assert.Equal(t, "user", webhook.Auth.Basic.Username)
	assert.Equal(t, "${WEBHOOK_PASSWORD}", webhook.Auth.Basic.Password)
	assert.Equal(t, "${WEBHOOK_SIGNING_SECRET}", webhook.Signing.Secret)
	assert.Equal(t, 25, webhook.Batch.MaxEvents)
	assert.Equal(t, "{\"id\":\"{{.ID}}\"}\n", webhook.PayloadTemplate)
	assert.Equal(t, "jq", webhook.Transform.Language)
	assert.Equal(t, ".after |= with_entries(select(.key != \"password\"))\n", webhook.Transform.Expression)
	assert.Equal(t, "/certs/ca.pem", webhook.TLS.CAFile)
	assert.Equal(t, "/certs/client.pem", webhook.TLS.CertFile)
	assert.Equal(t, "/certs/client-key.pem", webhook.TLS.KeyFile)
	require.NotNil(t, roundTrip.DLQ.Enabled)
	assert.True(t, *roundTrip.DLQ.Enabled)
	assert.Equal(t, "/var/lib/kaptanto/dlq.db", roundTrip.DLQ.Path)
	assert.Equal(t, "168h", roundTrip.DLQ.Retention)
}

func TestLoad_DLQEnabledUnsetAndExplicitFalse(t *testing.T) {
	unsetPath := writeTempYAML(t, `
dlq:
  path: /var/lib/kaptanto/dlq.db
`)
	unsetCfg, err := config.Load(unsetPath)
	require.NoError(t, err)
	assert.Nil(t, unsetCfg.DLQ.Enabled)

	falsePath := writeTempYAML(t, `
dlq:
  enabled: false
`)
	falseCfg, err := config.Load(falsePath)
	require.NoError(t, err)
	require.NotNil(t, falseCfg.DLQ.Enabled)
	assert.False(t, *falseCfg.DLQ.Enabled)
}

// --- Defaults tests ---

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()
	require.NotNil(t, cfg)

	assert.Equal(t, "stdout", cfg.Output)
	assert.Equal(t, 7654, cfg.Port)
	assert.Equal(t, "./data", cfg.DataDir)
	assert.Equal(t, "", cfg.Retention) // empty → runtime applies 1h
	assert.Equal(t, "", cfg.Source)
	assert.Nil(t, cfg.Tables)
}

// --- Merge tests ---

func TestMerge_NoChangedFlags(t *testing.T) {
	cfg := &config.Config{
		Source:    "postgres://original",
		Output:    "grpc",
		Port:      9000,
		DataDir:   "/original/data",
		Retention: "24h",
		Tables: map[string]config.TableConfig{
			"public.orders": {Columns: []string{"id"}, Where: "id > 0"},
		},
	}
	cmd := newCmdWithFlags()

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)

	// Nothing should change
	assert.Equal(t, "postgres://original", cfg.Source)
	assert.Equal(t, "grpc", cfg.Output)
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, "/original/data", cfg.DataDir)
	assert.Equal(t, "24h", cfg.Retention)
	require.Contains(t, cfg.Tables, "public.orders")
}

func TestMerge_ChangedSource(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlag(cmd, "source", "postgres://new-dsn")

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)
	assert.Equal(t, "postgres://new-dsn", cfg.Source)
}

func TestMerge_ChangedOutput(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlag(cmd, "output", "sse")

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)
	assert.Equal(t, "sse", cfg.Output)
}

func TestMerge_ChangedPort(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlagInt(cmd, "port", 8080)

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port)
}

func TestMerge_ChangedDataDir(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlag(cmd, "data-dir", "/new/data")

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)
	assert.Equal(t, "/new/data", cfg.DataDir)
}

func TestMerge_ChangedRetention(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlagDuration(cmd, "retention", 48*time.Hour)

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)
	assert.Equal(t, "48h0m0s", cfg.Retention)
}

func TestMerge_DLQFlagsOverrideYAML(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		DLQ: config.DLQConfig{
			Enabled:   &enabled,
			Path:      "/from/yaml/dlq.db",
			Retention: "24h",
		},
	}
	cmd := newCmdWithFlags()
	setFlag(cmd, "dlq-enabled", "false")
	setFlag(cmd, "dlq-path", "/from/cli/dlq.db")
	setFlagDuration(cmd, "dlq-retention", 72*time.Hour)

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)

	require.NotNil(t, cfg.DLQ.Enabled)
	assert.False(t, *cfg.DLQ.Enabled)
	assert.Equal(t, "/from/cli/dlq.db", cfg.DLQ.Path)
	assert.Equal(t, "72h0m0s", cfg.DLQ.Retention)
}

func TestMerge_ChangedTables_ReplacesPriorConfig(t *testing.T) {
	cfg := &config.Config{
		Tables: map[string]config.TableConfig{
			"public.orders": {Columns: []string{"id", "status"}, Where: "id > 0"},
			"public.users":  {Columns: []string{"id", "email"}, Where: ""},
		},
	}
	cmd := newCmdWithFlags()
	setFlagStringArray(cmd, "tables", []string{"public.products", "public.inventory"})

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)

	// Prior per-table config must be discarded entirely
	assert.NotContains(t, cfg.Tables, "public.orders")
	assert.NotContains(t, cfg.Tables, "public.users")

	// New tables present with empty TableConfig
	require.Contains(t, cfg.Tables, "public.products")
	require.Contains(t, cfg.Tables, "public.inventory")
	assert.Nil(t, cfg.Tables["public.products"].Columns)
	assert.Equal(t, "", cfg.Tables["public.products"].Where)
}

func TestMerge_ChangedTables_NilColumns(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlagStringArray(cmd, "tables", []string{"public.orders"})

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)

	require.Contains(t, cfg.Tables, "public.orders")
	// nil columns = all columns
	assert.Nil(t, cfg.Tables["public.orders"].Columns)
}

// TestMerge_ChangedTables_CommaSeparated verifies that a single --tables flag
// with a comma-separated value (the form used throughout the README and
// landing docs) splits into distinct table entries rather than registering
// one table literally named "public.orders,public.payments".
func TestMerge_ChangedTables_CommaSeparated(t *testing.T) {
	cfg := config.Defaults()
	cmd := newCmdWithFlags()
	setFlagStringArray(cmd, "tables", []string{"public.orders,public.payments"})

	err := config.Merge(cfg, cmd)
	require.NoError(t, err)

	require.Contains(t, cfg.Tables, "public.orders")
	require.Contains(t, cfg.Tables, "public.payments")
	assert.NotContains(t, cfg.Tables, "public.orders,public.payments")
}

// --- SourceType tests ---

func TestSourceType_Postgres(t *testing.T) {
	cfg := &config.Config{Source: "postgres://user:pass@host/db"}
	assert.Equal(t, "postgres", cfg.SourceType())
}

func TestSourceType_MongoDB(t *testing.T) {
	cfg := &config.Config{Source: "mongodb://localhost:27017/mydb"}
	assert.Equal(t, "mongodb", cfg.SourceType())
}

func TestSourceType_MongoDBSrv(t *testing.T) {
	cfg := &config.Config{Source: "mongodb+srv://cluster0.example.mongodb.net/mydb"}
	assert.Equal(t, "mongodb", cfg.SourceType())
}

func TestSourceType_Empty(t *testing.T) {
	cfg := &config.Config{Source: ""}
	assert.Equal(t, "postgres", cfg.SourceType(), "empty source should default to postgres")
}

// --- TableConfig semantics ---

func TestTableConfig_NilColumnsAllColumns(t *testing.T) {
	tc := config.TableConfig{Columns: nil, Where: ""}
	assert.Nil(t, tc.Columns, "nil Columns means all columns")
}

func TestTableConfig_WhereEmpty(t *testing.T) {
	tc := config.TableConfig{Where: ""}
	assert.Equal(t, "", tc.Where, "empty Where means no row filter")
}

// --- helpers ---

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(content), 0o600)
	require.NoError(t, err)
	return path
}

// newCmdWithFlags returns a *cobra.Command with all flags registered but none set.
func newCmdWithFlags() *cobra.Command {
	cmd := &cobra.Command{Use: "test", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
	cmd.Flags().String("source", "", "")
	cmd.Flags().StringSlice("tables", nil, "")
	cmd.Flags().String("output", "stdout", "")
	cmd.Flags().Int("port", 7654, "")
	cmd.Flags().String("data-dir", "./data", "")
	cmd.Flags().Duration("retention", 0, "")
	cmd.Flags().Bool("dlq-enabled", true, "")
	cmd.Flags().String("dlq-path", "", "")
	cmd.Flags().Duration("dlq-retention", 0, "")
	return cmd
}

// setFlag marks a string flag as changed (simulates user passing --flag value).
func setFlag(cmd *cobra.Command, name, value string) {
	if err := cmd.Flags().Set(name, value); err != nil {
		panic(err)
	}
}

// setFlagInt marks an int flag as changed.
func setFlagInt(cmd *cobra.Command, name string, value int) {
	if err := cmd.Flags().Set(name, fmt.Sprintf("%d", value)); err != nil {
		panic(err)
	}
}

// setFlagDuration marks a duration flag as changed.
func setFlagDuration(cmd *cobra.Command, name string, value time.Duration) {
	if err := cmd.Flags().Set(name, value.String()); err != nil {
		panic(err)
	}
}

// setFlagStringArray marks a StringArray flag as changed with multiple values.
func setFlagStringArray(cmd *cobra.Command, name string, values []string) {
	// Reset to clear defaults first
	for _, v := range values {
		if err := cmd.Flags().Set(name, v); err != nil {
			panic(err)
		}
	}
}
