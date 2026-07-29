package cmd

import (
	"context"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mcpMemEventLog struct{}

func (mcpMemEventLog) Append(*event.ChangeEvent) (uint64, error) { return 1, nil }
func (mcpMemEventLog) AppendBatch([]*event.ChangeEvent) ([]uint64, error) {
	return nil, nil
}
func (mcpMemEventLog) ReadPartition(context.Context, uint32, uint64, int) ([]eventlog.LogEntry, error) {
	return nil, nil
}
func (mcpMemEventLog) Close() error { return nil }

func TestOpenMCPServer_DisabledZeroCost(t *testing.T) {
	cfg := config.Defaults()
	cfg.MCP.Enabled = false
	s, err := openMCPServer(cfg, nil)
	require.NoError(t, err)
	assert.Nil(t, s)
}

func TestOpenMCPServer_EnabledWiresRecentAndShutdown(t *testing.T) {
	t.Setenv("MCP_CMD_WIRE", "cmd-wire-secret")

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Insecure = true
	cfg.MCP.Enabled = true
	cfg.MCP.APIKeys = []config.MCPAPIKey{{
		Name:   "agent",
		Key:    "${MCP_CMD_WIRE}",
		Tables: []string{"*"},
	}}
	cfg.Tables = map[string]config.TableConfig{
		"public.orders": {},
	}

	metrics := observability.NewKaptantoMetrics()
	s, err := openMCPServer(cfg, metrics)
	require.NoError(t, err)
	require.NotNil(t, s)

	rtr := router.NewRouter(mcpMemEventLog{}, 2, nil)
	assert.Equal(t, 0, rtr.ConsumerCount())

	s.SetRouter(rtr)
	assert.True(t, s.RecentIndexActive())
	assert.Equal(t, 1, rtr.ConsumerCount(), "enabled MCP must register internal recent consumer")

	require.NoError(t, s.Close())
	assert.Equal(t, 0, rtr.ConsumerCount(), "shutdown must unregister MCP consumers")
	assert.False(t, s.RecentIndexActive())
}

func TestOpenMCPServer_SharesServerTLS(t *testing.T) {
	t.Setenv("MCP_CMD_TLS", "tls-secret")

	dir := t.TempDir()
	cert, key := generateSelfSignedCert(t, dir)

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.MCP.Enabled = true
	cfg.MCP.APIKeys = []config.MCPAPIKey{{Name: "agent", Key: "${MCP_CMD_TLS}", Tables: []string{"*"}}}
	cfg.ServerTLS = config.ServerTLSConfig{
		CertFile: cert,
		KeyFile:  key,
	}

	s, err := openMCPServer(cfg, observability.NewKaptantoMetrics())
	require.NoError(t, err)
	require.NotNil(t, s)
	defer func() { _ = s.Close() }()
}
