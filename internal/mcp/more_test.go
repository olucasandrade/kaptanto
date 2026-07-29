package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_OwnsAuditorAndAccessors(t *testing.T) {
	t.Setenv("MCP_OWN_AUD", "tok")
	dir := t.TempDir()
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "agent", Key: "${MCP_OWN_AUD}", Tables: []string{"*"}}},
		},
		DataDir: dir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	defer func() { _ = s.Close() }()

	assert.NotNil(t, s.SDK())
	assert.NotNil(t, s.Auditor())
	require.Len(t, s.Keys(), 1)
	assert.Equal(t, "agent", s.Keys()[0].Name)
	assert.Equal(t, "agent", s.Keys()[0].ACL.Name())
	assert.Contains(t, dir, filepath.Base(dir))
	_ = s.Close()
	assert.NoError(t, s.Close()) // idempotent
}
func TestApply_NilAndPassthrough(t *testing.T) {
	acl, err := mcp.CompileACL(config.MCPAPIKey{Name: "a", Tables: []string{"*"}})
	require.NoError(t, err)
	out, ok := acl.Apply(nil)
	assert.False(t, ok)
	assert.Nil(t, out)

	ev := &event.ChangeEvent{
		Table:     "t",
		Operation: event.OpInsert,
		After:     json.RawMessage(`{"id":1}`),
		Before:    nil,
	}
	out, ok = acl.Apply(ev)
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`{"id":1}`), out.After)
	assert.Nil(t, out.Before)

	// Non-object after: pass through.
	ev2 := &event.ChangeEvent{
		Table:     "t",
		Operation: event.OpInsert,
		After:     json.RawMessage(`[1,2,3]`),
	}
	acl2, err := mcp.CompileACL(config.MCPAPIKey{
		Name:   "b",
		Tables: []string{"*"},
		Redact: []config.MCPRedactConfig{{
			Columns: []string{"id"},
		}},
	})
	require.NoError(t, err)
	out2, ok := acl2.Apply(ev2)
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`[1,2,3]`), out2.After)
}

func TestResolveConfig_DuplicateAndEmptyName(t *testing.T) {
	t.Setenv("MCP_DUP", "x")
	_, _, err := mcp.ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{
			{Name: "a", Key: "${MCP_DUP}", Tables: []string{"*"}},
			{Name: "a", Key: "${MCP_DUP}", Tables: []string{"*"}},
		},
	}, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	_, _, err = mcp.ResolveConfig(config.MCPConfig{
		Enabled: true,
		APIKeys: []config.MCPAPIKey{{Name: "  ", Key: "${MCP_DUP}"}},
	}, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestCompileACL_BadRedactGlob(t *testing.T) {
	_, err := mcp.CompileACL(config.MCPAPIKey{
		Name:   "k",
		Tables: []string{"*"},
		Redact: []config.MCPRedactConfig{{
			Tables:  []string{"a*b*c"},
			Columns: []string{"x"},
		}},
	})
	require.Error(t, err)
}

func TestNew_AuditDisabled(t *testing.T) {
	t.Setenv("MCP_NOAUD", "tok")
	falseVal := false
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_NOAUD}", Tables: []string{"*"}}},
			Audit:   config.MCPAuditConfig{Enabled: &falseVal},
		},
		DataDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Nil(t, s.Auditor())
	_ = s.Close()
}

func TestServer_RunShutdown(t *testing.T) {
	t.Setenv("MCP_RUN", "tok")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s, err := mcp.New(mcp.Options{
		Config: config.MCPConfig{
			Enabled: true,
			APIKeys: []config.MCPAPIKey{{Name: "a", Key: "${MCP_RUN}", Tables: []string{"*"}}},
			Audit:   config.MCPAuditConfig{Enabled: boolPtr(false)},
		},
		DataDir:  t.TempDir(),
		Listener: ln,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return s.Addr() != nil }, time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestAuditor_RecordClosed(t *testing.T) {
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	path := filepath.Join(t.TempDir(), "a.ndjson")
	a, err := mcp.NewAuditor(path, log)
	require.NoError(t, err)
	require.NoError(t, a.Close())
	a.Record(mcp.AuditRecord{Key: "k", Tool: "t", Outcome: mcp.OutcomeOK})
	assert.Contains(t, logBuf.String(), "write failed")
}

func boolPtr(v bool) *bool { return &v }
