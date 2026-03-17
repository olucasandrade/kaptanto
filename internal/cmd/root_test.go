package cmd_test

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/cmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpContainsKaptanto(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--help"}, buf)
	// --help returns an error from cobra (pflag.ErrHelp) but that's expected;
	// what matters is output contains "kaptanto"
	_ = err
	out := buf.String()
	assert.Contains(t, out, "kaptanto", "help output should contain 'kaptanto'")
}

func TestHelpContainsAllFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	_ = cmd.ExecuteWithArgs([]string{"--help"}, buf)
	out := buf.String()

	flags := []string{
		"--source",
		"--tables",
		"--output",
		"--port",
		"--config",
		"--data-dir",
		"--retention",
		"--ha",
		"--node-id",
		"--log-level",
	}
	for _, f := range flags {
		assert.Contains(t, out, f, "help output should contain flag %s", f)
	}
}

func TestFlagSource(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("source")
	require.NotNil(t, f, "flag 'source' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagTables(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("tables")
	require.NotNil(t, f, "flag 'tables' must exist")
	assert.Equal(t, "stringArray", f.Value.Type())
}

func TestFlagOutput(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("output")
	require.NotNil(t, f, "flag 'output' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "stdout", f.DefValue)
}

func TestFlagPort(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("port")
	require.NotNil(t, f, "flag 'port' must exist")
	assert.Equal(t, "int", f.Value.Type())
	assert.Equal(t, "7654", f.DefValue)
}

func TestFlagConfig(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("config")
	require.NotNil(t, f, "flag 'config' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagDataDir(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("data-dir")
	require.NotNil(t, f, "flag 'data-dir' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "./data", f.DefValue)
}

func TestFlagRetention(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("retention")
	require.NotNil(t, f, "flag 'retention' must exist")
	assert.Equal(t, "duration", f.Value.Type())
	// default is 0 (no retention limit applied at flag layer)
	assert.Equal(t, "0s", f.DefValue)
}

func TestFlagHA(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("ha")
	require.NotNil(t, f, "flag 'ha' must exist")
	assert.Equal(t, "bool", f.Value.Type())
	assert.Equal(t, "false", f.DefValue)
}

func TestFlagNodeID(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("node-id")
	require.NotNil(t, f, "flag 'node-id' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagLogLevel(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("log-level")
	require.NotNil(t, f, "flag 'log-level' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "info", f.DefValue)
}

func TestRetentionFlagType(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("retention")
	require.NotNil(t, f)
	// Parse a duration value to verify the flag accepts durations
	err := f.Value.Set("1h")
	require.NoError(t, err)
	v, err := time.ParseDuration(f.Value.String())
	require.NoError(t, err)
	assert.Equal(t, time.Hour, v)
}

// TestRunE_MissingSourceAndConfig verifies the guard condition: when neither
// --source nor --config is provided, RunE returns an error containing
// "--source or --config is required".
func TestRunE_MissingSourceAndConfig(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs(nil, buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source or --config is required")
}

// TestRunE_EmptySource verifies that explicitly passing an empty --source is
// treated as not set (the guard catches it).
func TestRunE_EmptySource(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--source", ""}, buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source or --config is required")
}

// TestRunE_ConfigFileNotFound verifies that --config pointing to a non-existent
// file returns an error (load config failure).
func TestRunE_ConfigFileNotFound(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--config", "/tmp/nonexistent_kaptanto_test.yaml"}, buf)
	require.Error(t, err)
}

// TestHAFlagHelpText verifies the --ha flag description references "advisory lock"
// and no longer references the outdated "etcd" mechanism.
func TestHAFlagHelpText(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("ha")
	require.NotNil(t, f, "flag 'ha' must exist")
	assert.NotContains(t, f.Usage, "etcd", "--ha help text must not mention etcd")
	assert.Contains(t, f.Usage, "advisory lock", "--ha help text must mention advisory lock")
}

// TestNonHAPathUnchanged verifies that running without --ha does not emit any
// "ha:" log lines, confirming the non-HA pipeline path is unaffected.
func TestNonHAPathUnchanged(t *testing.T) {
	// Skip if no POSTGRES_TEST_DSN — this test would start a real pipeline.
	if os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Skip("POSTGRES_TEST_DSN not set; skipping non-HA path test")
	}

	var buf bytes.Buffer
	// Run with a source but HA=false (default). The pipeline will fail quickly
	// when it can't connect to Postgres, but we only care that no "ha:" lines
	// appear before the failure.
	_ = cmd.ExecuteWithArgs([]string{"--source", os.Getenv("POSTGRES_TEST_DSN")}, &buf)
	out := buf.String()
	assert.False(t, strings.Contains(out, "ha:"), "non-HA path must not emit ha: log lines; got: %s", out)
}

// TestMongoDBFlagRoute verifies that when --source starts with "mongodb://",
// runPipeline takes the MongoDB branch (not the Postgres branch). The test
// confirms this by checking the error is a connection error, not the
// "source is required" guard error.
//
// serverSelectionTimeoutMS=500 makes the MongoDB driver fail fast when the
// server is unreachable, so the test completes in under 2s.
func TestMongoDBFlagRoute(t *testing.T) {
	// Use an unreachable MongoDB URI so the pipeline fails at connect time,
	// proving the MongoDB branch was entered (not the Postgres branch).
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "mongodb://127.0.0.1:27999/testdb?serverSelectionTimeoutMS=500",
		"--tables", "testcoll",
	}, &buf)
	// Must return an error (connect failure), NOT the "source is required" guard.
	require.Error(t, err, "mongodb pipeline must fail (no server at 27999)")
	assert.NotContains(t, err.Error(), "--source or --config is required",
		"error must be a connect failure, not the source guard")
	assert.NotContains(t, err.Error(), "source is required",
		"error must be a connect failure, not the source guard")
}

// TestMongoDBWithHAReturnsError verifies that passing --ha with a MongoDB
// source DSN returns a clear error before any Postgres connection is attempted.
// INT-03 gap closure.
func TestMongoDBWithHAReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "mongodb://127.0.0.1:27999/testdb",
		"--ha",
		"--tables", "testcoll",
	}, &buf)
	require.Error(t, err, "mongodb + --ha must return an error")
	assert.Contains(t, err.Error(), "ha:", "error must be prefixed ha:")
	assert.Contains(t, err.Error(), "Postgres", "error must mention Postgres requirement")
	assert.Contains(t, err.Error(), "mongodb", "error must mention the detected source type")
}

// TestHAFlagSkipsWithoutDSN verifies that --ha without POSTGRES_TEST_DSN
// causes the pipeline to return an error (connection failure), not a panic.
// This is an integration guard: when no Postgres is available the HA path
// should fail gracefully.
func TestHAFlagSkipsWithoutDSN(t *testing.T) {
	if os.Getenv("POSTGRES_TEST_DSN") != "" {
		t.Skip("POSTGRES_TEST_DSN is set; skipping graceful-skip test")
	}
	// Use an obviously unreachable DSN to trigger a connect error.
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--ha",
	}, &buf)
	// The HA path should return an error from NewLeaderElector or OpenPostgres,
	// not panic and not emit the old slog.Warn stub.
	require.Error(t, err, "HA mode with unreachable DB must return an error")
	assert.NotContains(t, err.Error(), "not yet implemented", "old slog.Warn stub must be gone")
}
