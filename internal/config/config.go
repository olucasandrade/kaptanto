// Package config provides typed YAML config loading, defaults, and CLI-flag merging.
//
// The config package is the single source of truth for runtime settings.
// It is intentionally free of global state: callers create Config values and
// pass them explicitly. This makes the package safe to use from tests without
// any setup/teardown.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// TableConfig holds per-table replication settings.
//
//   - Columns nil means replicate all columns (allow-all).
//   - Columns non-nil is a column allow-list.
//   - Where "" means no row filter.
//   - Where non-empty is a SQL WHERE expression applied to replicated rows.
type TableConfig struct {
	Columns []string `yaml:"columns"`
	Where   string   `yaml:"where"`
}

// Config is the complete runtime configuration for a kaptanto pipeline.
// YAML tags match the locked schema described in the project specification.
type Config struct {
	Source    string                 `yaml:"source"`
	Tables    map[string]TableConfig `yaml:"tables"`
	Output    string                 `yaml:"output"`
	Port      int                    `yaml:"port"`
	DataDir   string                 `yaml:"data-dir"`
	Retention string                 `yaml:"retention"` // stored as string; "" means use runtime default (1h)
	HA        bool                   `yaml:"ha"`        // CFG-01: --ha flag; Phase 8 leader election
	NodeID    string                 `yaml:"node-id"`   // CFG-01: --node-id flag; Phase 8 node identity
}

// SourceType returns the detected source database type based on the DSN prefix.
// Returns "mongodb" for mongodb:// and mongodb+srv:// URIs, "postgres" otherwise.
func (c *Config) SourceType() string {
	if strings.HasPrefix(c.Source, "mongodb://") || strings.HasPrefix(c.Source, "mongodb+srv://") {
		return "mongodb"
	}
	return "postgres"
}

// Load reads the YAML file at path and unmarshals it into a new Config.
// Returns a non-nil error for missing files or malformed YAML.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	return &cfg, nil
}

// Defaults returns a *Config populated with sensible zero values.
//
// Retention is intentionally "" rather than "1h": the Event Log initializer
// applies the 1h default at runtime so that an explicit --retention 0 in the
// config file is distinguishable from "not set at all".
func Defaults() *Config {
	return &Config{
		Output:  "stdout",
		Port:    7654,
		DataDir: "./data",
	}
}

// Merge applies only the cobra flags that were explicitly set by the user
// (cmd.Flags().Changed()) on top of cfg. Flags that were not passed by the
// user are ignored, preserving the values already in cfg (from Load or Defaults).
//
// Special behaviour for --tables: when Changed, the entire cfg.Tables map is
// replaced with new entries (empty TableConfig) for each named table. Any
// per-table config loaded from a YAML file is discarded — the flag-level
// intent is "replicate exactly these tables with no filtering".
func Merge(cfg *Config, cmd *cobra.Command) error {
	flags := cmd.Flags()

	if flags.Changed("source") {
		v, err := flags.GetString("source")
		if err != nil {
			return fmt.Errorf("config: merge source: %w", err)
		}
		cfg.Source = v
	}

	if flags.Changed("output") {
		v, err := flags.GetString("output")
		if err != nil {
			return fmt.Errorf("config: merge output: %w", err)
		}
		cfg.Output = v
	}

	if flags.Changed("port") {
		v, err := flags.GetInt("port")
		if err != nil {
			return fmt.Errorf("config: merge port: %w", err)
		}
		cfg.Port = v
	}

	if flags.Changed("data-dir") {
		v, err := flags.GetString("data-dir")
		if err != nil {
			return fmt.Errorf("config: merge data-dir: %w", err)
		}
		cfg.DataDir = v
	}

	if flags.Changed("retention") {
		v, err := flags.GetDuration("retention")
		if err != nil {
			return fmt.Errorf("config: merge retention: %w", err)
		}
		cfg.Retention = v.String()
	}

	if flags.Changed("tables") {
		names, err := flags.GetStringArray("tables")
		if err != nil {
			return fmt.Errorf("config: merge tables: %w", err)
		}
		// Replace the entire tables map; discard any per-table config from file.
		newTables := make(map[string]TableConfig, len(names))
		for _, name := range names {
			newTables[name] = TableConfig{}
		}
		cfg.Tables = newTables
	}

	if flags.Changed("ha") {
		v, err := flags.GetBool("ha")
		if err != nil {
			return fmt.Errorf("config: merge ha: %w", err)
		}
		cfg.HA = v
	}

	if flags.Changed("node-id") {
		v, err := flags.GetString("node-id")
		if err != nil {
			return fmt.Errorf("config: merge node-id: %w", err)
		}
		cfg.NodeID = v
	}

	return nil
}
