package mcp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditor_GoldenLine(t *testing.T) {
	var buf bytes.Buffer
	a := NewAuditorWriter(&buf, slog.Default())
	fixed := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	a.SetNow(func() time.Time { return fixed })

	a.Record(AuditRecord{
		Key:           "support",
		Tool:          "list_tables",
		ParamsSummary: []string{},
		Tables:        []string{"public.orders"},
		Outcome:       OutcomeOK,
		DurationMS:    3,
	})

	want := `{"ts":"2026-07-21T12:00:00Z","key":"support","tool":"list_tables","params_summary":[],"tables":["public.orders"],"outcome":"ok","duration_ms":3}` + "\n"
	assert.Equal(t, want, buf.String())

	var got AuditRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))
	assert.Equal(t, "support", got.Key)
	assert.Equal(t, OutcomeOK, got.Outcome)
	assert.NotContains(t, buf.String(), "secret")
	assert.NotContains(t, buf.String(), "password")
}

func TestAuditor_NilLoggerAndExplicitTS(t *testing.T) {
	a := NewAuditorWriter(&bytes.Buffer{}, nil)
	a.Record(AuditRecord{
		TS:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 3600)),
		Key:     "k",
		Tool:    "t",
		Outcome: OutcomeOK,
	})
	path := filepath.Join(t.TempDir(), "sub", "a.ndjson")
	a2, err := NewAuditor(path, nil)
	require.NoError(t, err)
	_ = a2.Close()
	// Close again on nil file.
	assert.NoError(t, a2.Close())
}

func TestAuditor_WriteFailureTolerated(t *testing.T) {
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewAuditorWriter(failWriter{}, log)
	a.Record(AuditRecord{Key: "unknown", Tool: "auth", Outcome: OutcomeDenied})
	assert.Contains(t, logBuf.String(), "mcp audit: write failed")
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, assert.AnError }

func TestAuditor_Rotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-audit.ndjson")
	a, err := NewAuditor(path, slog.Default())
	require.NoError(t, err)
	defer func() { _ = a.Close() }()

	a.Record(AuditRecord{Key: "k", Tool: "t", Outcome: OutcomeOK})
	require.NoError(t, a.ForceRotateForTest())
	a.Record(AuditRecord{Key: "k", Tool: "t2", Outcome: OutcomeOK})

	_, err = os.Stat(path + ".1")
	require.NoError(t, err, "rotated file should exist")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"tool":"t2"`)

	// Size-triggered path.
	a.SetSizeForTest(auditMaxBytes)
	a.Record(AuditRecord{Key: "k", Tool: "t3", Outcome: OutcomeOK})
	_, err = os.Stat(path + ".1")
	require.NoError(t, err)
}

func TestAuditor_KeepLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	a, err := NewAuditor(path, slog.Default())
	require.NoError(t, err)
	defer func() { _ = a.Close() }()

	for i := 0; i < auditKeep+2; i++ {
		a.Record(AuditRecord{Key: "k", Tool: "t", Outcome: OutcomeOK, DurationMS: int64(i)})
		require.NoError(t, a.ForceRotateForTest())
	}
	// After keep+2 rotations, .4 should not exist (keep=3).
	_, err = os.Stat(path + ".4")
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(path + ".3")
	require.NoError(t, err)
}
