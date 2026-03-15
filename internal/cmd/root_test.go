package cmd_test

import (
	"bytes"
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
