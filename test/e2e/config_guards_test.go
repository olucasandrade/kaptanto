//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_VectorConfigRejected verifies the binary rejects --output vector
// without a sinks.vector block before attempting to connect to Postgres.
func TestE2E_VectorConfigRejected(t *testing.T) {
	bin := buildBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "vector",
		"--all-tables",
		"--data-dir", t.TempDir(),
		"--insecure",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.Error(t, err, "vector output without sinks.vector must fail fast")
	combined := stderr.String()
	assert.Contains(t, combined, "sinks.vector")
}

// TestE2E_MCPDisabledNoListen verifies MCP zero-cost when disabled: when MCP is
// explicitly disabled, the configured MCP port does not accept connections.
func TestE2E_MCPDisabledNoListen(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (logical-replication Postgres) to run e2e tests")
	}

	fx := setupE2ETable(t, dsn, "id int PRIMARY KEY, status text")
	bin := buildBinary(t)
	dataDir := t.TempDir()
	mcpPort := freePort(t)

	cfgPath := filepath.Join(t.TempDir(), "kaptanto.yaml")
	cfg := fmt.Sprintf(`source: %s
output: stdout
source-id: %s
data-dir: %s
insecure: true
tables:
  public.%s: {}
mcp:
  enabled: false
  port: %d
`, dsn, fx.SourceID, dataDir, fx.Table, mcpPort)
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, "--config", cfgPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	collector := newEventCollector()
	tailNDJSON(stdout, collector, fx.Table, dmlOps)

	waitForReplicationSlot(t, fx.Conn, fx.SourceID, 30*time.Second)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", mcpPort), 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("unexpected MCP listener on port %d when mcp.enabled is false", mcpPort)
	}
}

// TestE2E_MCPEnabledMissingKeyRejected verifies enabled MCP without
// resolvable api-keys fails during config validation.
func TestE2E_MCPEnabledMissingKeyRejected(t *testing.T) {
	bin := buildBinary(t)
	cfgPath := filepath.Join(t.TempDir(), "kaptanto.yaml")
	cfg := []byte(`source: postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test
output: none
data-dir: /tmp/kaptanto-e2e
insecure: true
all-tables: true
mcp:
  enabled: true
  api-keys:
    - name: agent
      key: ${MCP_API_KEY}
      tables: ["*"]
`)
	require.NoError(t, os.WriteFile(cfgPath, cfg, 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", cfgPath, "--data-dir", t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.Error(t, err, "enabled MCP with unset MCP_API_KEY must fail")
	assert.Contains(t, stderr.String(), "MCP_API_KEY")
}
