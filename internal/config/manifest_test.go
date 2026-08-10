package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteConfigManifest refreshes landing/test/fixtures/valid-outputs.json
// when run with UPDATE_CONFIG_MANIFEST=1.
func TestWriteConfigManifest(t *testing.T) {
	manifest := map[string][]string{
		"outputs": ValidOutputModes(),
		"sinks":   ValidSinkKeys(),
	}
	if os.Getenv("UPDATE_CONFIG_MANIFEST") == "1" {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		require.NoError(t, err)
		path := filepath.Join(root, "landing", "test", "fixtures", "valid-outputs.json")
		data, err := json.MarshalIndent(manifest, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
		t.Logf("updated %s", path)
	}
}
