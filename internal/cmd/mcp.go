package cmd

import (
	"fmt"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/mcp"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// openMCPServer builds the optional MCP listener. Returns (nil, nil) when
// mcp.enabled is false (MCP-04 zero cost).
func openMCPServer(cfg *config.Config, metrics *observability.KaptantoMetrics) (*mcp.Server, error) {
	if !cfg.MCP.Enabled {
		return nil, nil
	}
	mcpTLS, err := buildServerTLSConfig(cfg.ServerTLS)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	if err := requireServerTLS("mcp", mcpTLS, cfg.Insecure); err != nil {
		return nil, err
	}
	return mcp.New(mcp.Options{
		Config:  cfg.MCP,
		DataDir: cfg.DataDir,
		TLS:     mcpTLS,
		Metrics: metrics,
	})
}
