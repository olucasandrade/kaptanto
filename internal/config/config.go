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
	"github.com/spf13/pflag"
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

// TLSConfig holds paths to TLS certificates for sink connections.
// All fields are optional; omit to use system CAs and no mutual TLS.
type TLSConfig struct {
	CAFile   string `yaml:"ca-file"`   // path to CA PEM; empty = system CAs
	CertFile string `yaml:"cert-file"` // client cert PEM for mutual TLS
	KeyFile  string `yaml:"key-file"`  // client key PEM for mutual TLS
}

// NATSSinkConfig holds connection settings for the NATS JetStream sink.
type NATSSinkConfig struct {
	URL             string    `yaml:"url"`              // e.g. "nats://localhost:4222"
	SubjectTemplate string    `yaml:"subject-template"` // Go template, e.g. "cdc.{{.Schema}}.{{.Table}}"
	StreamName      string    `yaml:"stream-name"`      // optional: if set, validated at startup
	TLS             TLSConfig `yaml:"tls"`
}

// SQSSinkConfig holds connection settings for the AWS SQS FIFO sink.
// QueueURL must be a FIFO queue URL (name must end in .fifo).
// Region is required. AccessKeyID and SecretAccessKey are optional static
// credentials; if absent, the full AWS credential chain is used
// (env vars → ~/.aws/credentials → IAM instance profile).
// TLS allows specifying a custom CA for VPC endpoints.
//
// QueueURLTemplate is an optional Go template for per-table routing
// (e.g. `https://sqs.us-east-1.amazonaws.com/123/cdc-{{.Schema}}-{{.Table}}.fifo`).
// When set, overrides QueueURL per-message.
//
// High-throughput FIFO mode is a queue-level AWS setting and does not
// require any config change here. See AWS docs for enabling it on the queue.
type SQSSinkConfig struct {
	QueueURL         string    `yaml:"queue-url"`
	QueueURLTemplate string    `yaml:"queue-url-template"` // optional Go template; overrides QueueURL per-message when set
	Region           string    `yaml:"region"`
	AccessKeyID      string    `yaml:"access-key-id"`
	SecretAccessKey  string    `yaml:"secret-access-key"`
	TLS              TLSConfig `yaml:"tls"`
}

// KafkaSinkConfig holds connection settings for the Apache Kafka sink.
// BootstrapServers is required (at least one broker address, e.g. "broker1:9092").
// TopicTemplate is a Go template applied per-event, e.g. "cdc.{{.Schema}}.{{.Table}}".
// SASLMechanism must be one of "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512", or "" (no SASL).
// TLS allows specifying custom CA / mTLS certificates.
type KafkaSinkConfig struct {
	BootstrapServers []string  `yaml:"bootstrap-servers"`
	TopicTemplate    string    `yaml:"topic-template"`
	SASLMechanism    string    `yaml:"sasl-mechanism"`
	SASLUsername     string    `yaml:"sasl-username"`
	SASLPassword     string    `yaml:"sasl-password"`
	TLS              TLSConfig `yaml:"tls"`
}

// PubSubSinkConfig holds connection settings for the Google Cloud Pub/Sub sink.
// ProjectID is required. TopicID is required.
// CredentialsFile is optional; when empty, Application Default Credentials (ADC)
// are used automatically (GOOGLE_APPLICATION_CREDENTIALS env var or
// gcloud auth application-default login).
// TopicTemplate is an optional Go template for per-event topic routing
// (e.g. "cdc-{{.Schema}}-{{.Table}}"). When empty, TopicID is used directly.
type PubSubSinkConfig struct {
	ProjectID       string `yaml:"project-id"`
	TopicID         string `yaml:"topic-id"`
	CredentialsFile string `yaml:"credentials-file"` // optional; empty = ADC
	TopicTemplate   string `yaml:"topic-template"`   // optional Go template
}

// RabbitMQSinkConfig holds connection settings for the RabbitMQ sink.
// URL is the AMQP or AMQPS connection URL, e.g. "amqp://user:pass@host:5672/"
// or "amqps://..." for TLS connections.
// Exchange is the exchange name; empty string uses the default exchange.
// RoutingKeyTemplate is a Go template applied per-event to compute the routing
// key, e.g. "cdc.{{.Schema}}.{{.Table}}".
// TLS allows specifying a custom CA for broker certificate verification and
// optional client certificates for mutual TLS.
type RabbitMQSinkConfig struct {
	URL                string    `yaml:"url"`                  // AMQP URL e.g. "amqp://user:pass@host:5672/"
	Exchange           string    `yaml:"exchange"`             // exchange name; empty = default exchange
	RoutingKeyTemplate string    `yaml:"routing-key-template"` // Go template e.g. "cdc.{{.Schema}}.{{.Table}}"
	TLS                TLSConfig `yaml:"tls"`
}

// SinksConfig holds connection settings for all supported queue sinks.
// Only the active sink's sub-block needs to be populated.
type SinksConfig struct {
	NATS     *NATSSinkConfig     `yaml:"nats"`
	SQS      *SQSSinkConfig      `yaml:"sqs"`
	Kafka    *KafkaSinkConfig    `yaml:"kafka"`
	PubSub   *PubSubSinkConfig   `yaml:"pubsub"`
	RabbitMQ *RabbitMQSinkConfig `yaml:"rabbitmq"`
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
	SourceID   string                 `yaml:"source-id"`   // logical name used for slot/publication naming (default: "default")
	Cluster         bool                   `yaml:"cluster"`          // --cluster flag; Phase 14 shared cursor state (PostgresCursorStore)
	ClusterDSN      string                 `yaml:"cluster-dsn"`      // --cluster-dsn flag; Postgres DSN for shared cursor store
	ClusterPeers    []string               `yaml:"cluster-peers"`    // NATS JetStream cluster peer addresses, e.g. ["node2:6222", "node3:6222"]
	NatsClusterPort int                    `yaml:"nats-cluster-port"` // NATS cluster route port; 0 → 6222 applied at runtime
	Sinks           SinksConfig            `yaml:"sinks"`             // queue sink connection settings
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

	for _, f := range []struct {
		name string
		dest *string
	}{
		{"source", &cfg.Source},
		{"output", &cfg.Output},
		{"data-dir", &cfg.DataDir},
		{"node-id", &cfg.NodeID},
		{"source-id", &cfg.SourceID},
		{"cluster-dsn", &cfg.ClusterDSN},
	} {
		if err := mergeString(flags, f.name, f.dest); err != nil {
			return err
		}
	}

	for _, f := range []struct {
		name string
		dest *int
	}{
		{"port", &cfg.Port},
		{"nats-cluster-port", &cfg.NatsClusterPort},
	} {
		if err := mergeInt(flags, f.name, f.dest); err != nil {
			return err
		}
	}

	for _, f := range []struct {
		name string
		dest *bool
	}{
		{"ha", &cfg.HA},
		{"cluster", &cfg.Cluster},
	} {
		if err := mergeBool(flags, f.name, f.dest); err != nil {
			return err
		}
	}

	if err := mergeStringSlice(flags, "cluster-peers", &cfg.ClusterPeers); err != nil {
		return err
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

	return nil
}

func mergeString(flags *pflag.FlagSet, name string, dest *string) error {
	if !flags.Changed(name) {
		return nil
	}
	v, err := flags.GetString(name)
	if err != nil {
		return fmt.Errorf("config: merge %s: %w", name, err)
	}
	*dest = v
	return nil
}

func mergeInt(flags *pflag.FlagSet, name string, dest *int) error {
	if !flags.Changed(name) {
		return nil
	}
	v, err := flags.GetInt(name)
	if err != nil {
		return fmt.Errorf("config: merge %s: %w", name, err)
	}
	*dest = v
	return nil
}

func mergeBool(flags *pflag.FlagSet, name string, dest *bool) error {
	if !flags.Changed(name) {
		return nil
	}
	v, err := flags.GetBool(name)
	if err != nil {
		return fmt.Errorf("config: merge %s: %w", name, err)
	}
	*dest = v
	return nil
}

func mergeStringSlice(flags *pflag.FlagSet, name string, dest *[]string) error {
	if !flags.Changed(name) {
		return nil
	}
	v, err := flags.GetStringSlice(name)
	if err != nil {
		return fmt.Errorf("config: merge %s: %w", name, err)
	}
	*dest = v
	return nil
}
