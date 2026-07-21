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

// WebhookSinkConfig holds settings for the HTTP webhook sink.
type WebhookSinkConfig struct {
	URL             string            `yaml:"url"`
	URLTemplate     string            `yaml:"url-template"` // optional; overrides URL per-event
	Method          string            `yaml:"method"`       // POST (default) | PUT | PATCH
	Timeout         string            `yaml:"timeout"`      // Go duration; "" = 30s
	Headers         map[string]string `yaml:"headers"`      // static headers; values support ${VAR}
	Auth            WebhookAuthConfig `yaml:"auth"`
	Signing         WebhookSigning    `yaml:"signing"`
	Batch           WebhookBatch      `yaml:"batch"`
	PayloadTemplate string            `yaml:"payload-template"` // requires batch.max-events == 1
	Transform       TransformConfig   `yaml:"transform"`
	TLS             TLSConfig         `yaml:"tls"`
}

// WebhookAuthConfig holds bearer or basic auth for the webhook sink.
type WebhookAuthConfig struct {
	BearerToken string           `yaml:"bearer-token"` // supports ${VAR}
	Basic       WebhookBasicAuth `yaml:"basic"`
}

// WebhookBasicAuth holds HTTP basic-auth credentials for the webhook sink.
type WebhookBasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"` // supports ${VAR}
}

// WebhookSigning holds HMAC-SHA256 signing settings for the webhook sink.
// Empty Secret disables signing.
type WebhookSigning struct {
	Secret string `yaml:"secret"` // HMAC-SHA256; supports ${VAR}; empty = signing disabled
}

// WebhookBatch controls how many events are sent per HTTP request.
// MaxEvents 0 or 1 means one request per event; >1 sends a JSON array body.
type WebhookBatch struct {
	MaxEvents int `yaml:"max-events"` // 0/1 = one request per event; >1 = JSON array body
}

// TransformConfig is the shared transform shape (used by the webhook sink in v1).
type TransformConfig struct {
	Language   string `yaml:"language"`   // "jq" | "go-template"
	Expression string `yaml:"expression"` // inline; YAML block scalars for multi-line
}

// DLQConfig is a top-level `dlq:` block (router-level feature, not per-sink).
// Enabled is a pointer so YAML can distinguish unset (nil → on by default)
// from an explicit false (pre-DLQ log-and-drop).
type DLQConfig struct {
	Enabled   *bool  `yaml:"enabled"`   // nil/true = on (default); false = pre-DLQ log-and-drop
	Path      string `yaml:"path"`      // default <data-dir>/dlq.db
	Retention string `yaml:"retention"` // Go duration; ""/0 = keep forever
}

// ActionConfig defines a single configured action instance.
// Actions produce router.Consumer instances via the action registry's
// BuildConsumers function. Each action references a registered Type (e.g.
// "slack", "pagerduty") and supplies parameters + optional routing match.
type ActionConfig struct {
	Name      string             `yaml:"name"`      // unique identifier ([a-z0-9-]+, no ":")
	Type      string             `yaml:"type"`      // registered action type name
	Params    map[string]string  `yaml:"params"`    // key/value params; secret params must be ${VAR} refs
	Match     MatchConfig        `yaml:"match"`     // event routing filter (RTG-01)
	Timeout   string             `yaml:"timeout"`   // override type's default timeout; Go duration
	Transform *TransformConfig   `yaml:"transform"` // replaces (not merges) the type's default transform
	Headers   map[string]string  `yaml:"headers"`   // merge over type defaults; must not collide with computed auth
	Batch     *WebhookBatch      `yaml:"batch"`     // override type's batching; rejected if type pins batch
	Webhook   *WebhookSinkConfig `yaml:"webhook"`   // verbatim webhook config for type "custom" (escape hatch)
}

// MatchConfig declares which events an action should receive.
// Empty Tables means "match all tables"; empty Operations means "match all operations";
// empty Where means "match all rows".
type MatchConfig struct {
	Tables     []string `yaml:"tables"`
	Operations []string `yaml:"operations"`
	Where      string   `yaml:"where"`
}

// VectorSourceConfig selects how text is extracted from a ChangeEvent.
// Exactly one of Columns or Template must be set.
type VectorSourceConfig struct {
	Columns  []string `yaml:"columns"`  // joined "col: value\n" in listed order from After
	Template string   `yaml:"template"` // go-template over ChangeEvent (text/template)
}

// VectorEmbedderConfig configures the embedding provider.
type VectorEmbedderConfig struct {
	Provider   string `yaml:"provider"`   // openai | cohere
	BaseURL    string `yaml:"base-url"`   // openai default https://api.openai.com/v1; Ollama: http://localhost:11434/v1
	APIKey     string `yaml:"api-key"`    // STRICT ${VAR} when set; MAY be empty (local endpoints)
	Model      string `yaml:"model"`      // required
	Dimensions int    `yaml:"dimensions"` // optional (openai dimensions param)
}

// VectorStoreConfig configures the vector store backend.
// Provider selects which fields are required:
//   - pgvector: dsn (${VAR}), table (default "kaptanto_vectors")
//   - pinecone: api-key (${VAR}), index-host, namespace (optional)
//   - qdrant: url, collection, api-key (${VAR}, optional)
type VectorStoreConfig struct {
	Provider   string `yaml:"provider"` // pgvector | pinecone | qdrant
	DSN        string `yaml:"dsn"`
	Table      string `yaml:"table"`
	APIKey     string `yaml:"api-key"`
	IndexHost  string `yaml:"index-host"`
	Namespace  string `yaml:"namespace"`
	URL        string `yaml:"url"`
	Collection string `yaml:"collection"`
}

// VectorBatchConfig controls how many events are flushed per embed+upsert call.
type VectorBatchConfig struct {
	MaxEvents int `yaml:"max-events"` // default 96; clamped to embedder Cap() at runtime
}

// VectorSinkConfig holds settings for the vector store sink (output: vector).
type VectorSinkConfig struct {
	Source   VectorSourceConfig   `yaml:"source"`
	Embedder VectorEmbedderConfig `yaml:"embedder"`
	Store    VectorStoreConfig    `yaml:"store"`
	Metadata []string             `yaml:"metadata"` // After column names copied into Record.Metadata
	Batch    VectorBatchConfig    `yaml:"batch"`
}

// SinksConfig holds connection settings for all supported queue sinks.
// Only the active sink's sub-block needs to be populated.
type SinksConfig struct {
	NATS     *NATSSinkConfig     `yaml:"nats"`
	SQS      *SQSSinkConfig      `yaml:"sqs"`
	Kafka    *KafkaSinkConfig    `yaml:"kafka"`
	PubSub   *PubSubSinkConfig   `yaml:"pubsub"`
	RabbitMQ *RabbitMQSinkConfig `yaml:"rabbitmq"`
	Webhook  *WebhookSinkConfig  `yaml:"webhook"`
	Vector   *VectorSinkConfig   `yaml:"vector"`
}

// ServerTLSConfig holds TLS settings for Kaptanto's own inbound servers
// (SSE and gRPC). This is distinct from TLSConfig which applies to outbound
// sink connections.
//
// CertFile and KeyFile are required to enable TLS. ClientCAFile is optional
// and enables mutual TLS (mTLS): when set, client certificates are required
// and verified against the given CA PEM.
//
// Cert rotation is not supported in v1; restart the process to reload certs.
type ServerTLSConfig struct {
	CertFile     string `yaml:"cert-file"`      // path to server certificate PEM; required for TLS
	KeyFile      string `yaml:"key-file"`       // path to server private key PEM; required for TLS
	ClientCAFile string `yaml:"client-ca-file"` // path to CA PEM for client cert verification (mTLS); optional
}

// Config is the complete runtime configuration for a kaptanto pipeline.
// YAML tags match the locked schema described in the project specification.
type Config struct {
	Source          string                 `yaml:"source"`
	Tables          map[string]TableConfig `yaml:"tables"`
	Output          string                 `yaml:"output"`
	Port            int                    `yaml:"port"`
	CORSOrigin      string                 `yaml:"cors-origin"` // SSE Access-Control-Allow-Origin; empty = no CORS header (no cross-origin browser access)
	DataDir         string                 `yaml:"data-dir"`
	Retention       string                 `yaml:"retention"`         // stored as string; "" means use runtime default (1h)
	HA              bool                   `yaml:"ha"`                // CFG-01: --ha flag; Phase 8 leader election
	NodeID          string                 `yaml:"node-id"`           // CFG-01: --node-id flag; Phase 8 node identity
	SourceID        string                 `yaml:"source-id"`         // logical name used for slot/publication naming (default: "default")
	AllowAllTables  bool                   `yaml:"all-tables"`        // --all-tables flag; explicit opt-in to FOR ALL TABLES publication when no tables are configured
	Cluster         bool                   `yaml:"cluster"`           // --cluster flag; Phase 14 shared cursor state (PostgresCursorStore)
	ClusterDSN      string                 `yaml:"cluster-dsn"`       // --cluster-dsn flag; Postgres DSN for shared cursor store
	ClusterPeers    []string               `yaml:"cluster-peers"`     // NATS JetStream cluster peer addresses, e.g. ["node2:6222", "node3:6222"]
	NatsClusterPort int                    `yaml:"nats-cluster-port"` // NATS cluster route port; 0 → 6222 applied at runtime
	Sinks           SinksConfig            `yaml:"sinks"`             // queue sink connection settings
	ServerTLS       ServerTLSConfig        `yaml:"server-tls"`        // inbound server TLS (SSE / gRPC); distinct from sink-side TLS
	AuthToken       string                 `yaml:"auth-token"`        // static bearer token for SSE/gRPC data plane; also read from KAPTANTO_AUTH_TOKEN env var
	Insecure        bool                   `yaml:"insecure"`          // allow plaintext/unauthenticated sse/grpc (loud warning at startup; not for production)
	DLQ             DLQConfig              `yaml:"dlq"`               // router-level dead-letter queue (enabled by default when Enabled is nil)
	Actions         []ActionConfig         `yaml:"actions"`           // configured action instances
}

// SourceType returns the detected source database type based on the DSN prefix.
// Returns "mongodb" for mongodb:// and mongodb+srv:// URIs, "postgres" otherwise.
func (c *Config) SourceType() string {
	if strings.HasPrefix(c.Source, "mongodb://") || strings.HasPrefix(c.Source, "mongodb+srv://") {
		return "mongodb"
	}
	return "postgres"
}

// ApplyEnv overlays environment-variable values onto cfg. Only variables that
// are set to a non-empty value override the corresponding field.
//
// Variables:
//   - KAPTANTO_AUTH_TOKEN → cfg.AuthToken (preferred over --auth-token argv so
//     the secret is not visible in process listings)
func ApplyEnv(cfg *Config) {
	if v := os.Getenv("KAPTANTO_AUTH_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
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
		{"cors-origin", &cfg.CORSOrigin},
		{"node-id", &cfg.NodeID},
		{"source-id", &cfg.SourceID},
		{"cluster-dsn", &cfg.ClusterDSN},
		{"tls-cert", &cfg.ServerTLS.CertFile},
		{"tls-key", &cfg.ServerTLS.KeyFile},
		{"tls-client-ca", &cfg.ServerTLS.ClientCAFile},
		{"auth-token", &cfg.AuthToken},
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
		{"all-tables", &cfg.AllowAllTables},
		{"insecure", &cfg.Insecure},
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

	if flags.Changed("dlq-enabled") {
		v, err := flags.GetBool("dlq-enabled")
		if err != nil {
			return fmt.Errorf("config: merge dlq-enabled: %w", err)
		}
		cfg.DLQ.Enabled = &v
	}
	if err := mergeString(flags, "dlq-path", &cfg.DLQ.Path); err != nil {
		return err
	}
	if flags.Changed("dlq-retention") {
		v, err := flags.GetDuration("dlq-retention")
		if err != nil {
			return fmt.Errorf("config: merge dlq-retention: %w", err)
		}
		cfg.DLQ.Retention = v.String()
	}

	if flags.Changed("tables") {
		names, err := flags.GetStringSlice("tables")
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
