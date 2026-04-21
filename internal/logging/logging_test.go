package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel_Debug(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, logging.ParseLevel("debug"))
}

func TestParseLevel_Info(t *testing.T) {
	assert.Equal(t, slog.LevelInfo, logging.ParseLevel("info"))
}

func TestParseLevel_Warn(t *testing.T) {
	assert.Equal(t, slog.LevelWarn, logging.ParseLevel("warn"))
}

func TestParseLevel_Error(t *testing.T) {
	assert.Equal(t, slog.LevelError, logging.ParseLevel("error"))
}

func TestParseLevel_Warning_CaseInsensitive(t *testing.T) {
	assert.Equal(t, slog.LevelWarn, logging.ParseLevel("WARNING"))
}

func TestParseLevel_Unknown_DefaultsToInfo(t *testing.T) {
	assert.Equal(t, slog.LevelInfo, logging.ParseLevel("unknown"))
	assert.Equal(t, slog.LevelInfo, logging.ParseLevel(""))
	assert.Equal(t, slog.LevelInfo, logging.ParseLevel("verbose"))
}

func TestSetup_ProducesJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.Setup(&buf, "info")
	require.NotNil(t, logger)

	logger.Info("test message", "key", "value")

	output := buf.String()
	require.NotEmpty(t, output, "expected log output but got nothing")

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &m), "log output must be valid JSON: %s", output)

	assert.Contains(t, m, "msg", "JSON output must contain 'msg' key")
	assert.Contains(t, m, "level", "JSON output must contain 'level' key")
	assert.Equal(t, "test message", m["msg"])
}

func TestSetup_ErrorLevelDoesNotEmitInfoMessages(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.Setup(&buf, "error")
	require.NotNil(t, logger)

	logger.Info("this should not appear")
	logger.Debug("this should not appear either")

	assert.Empty(t, buf.String(), "error-level logger must not emit info or debug messages")

	logger.Error("this should appear")
	assert.NotEmpty(t, buf.String(), "error-level logger must emit error messages")
}
