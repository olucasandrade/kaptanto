// Package config_test provides TDD tests for the sinks config types.
package config_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestSinksConfig_FullNATSBlock tests that a full sinks.nats YAML block
// is parsed into Config.Sinks.NATS correctly.
func TestSinksConfig_FullNATSBlock(t *testing.T) {
	raw := `
sinks:
  nats:
    url: "nats://localhost:4222"
    subject-template: "cdc.{{.Schema}}.{{.Table}}"
    stream-name: "CDC_EVENTS"
    tls:
      ca-file: "/etc/certs/ca.pem"
      cert-file: "/etc/certs/client.pem"
      key-file: "/etc/certs/client-key.pem"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	require.NotNil(t, cfg.Sinks.NATS, "NATS config should be non-nil when block is present")
	assert.Equal(t, "nats://localhost:4222", cfg.Sinks.NATS.URL)
	assert.Equal(t, "cdc.{{.Schema}}.{{.Table}}", cfg.Sinks.NATS.SubjectTemplate)
	assert.Equal(t, "CDC_EVENTS", cfg.Sinks.NATS.StreamName)
	assert.Equal(t, "/etc/certs/ca.pem", cfg.Sinks.NATS.TLS.CAFile)
	assert.Equal(t, "/etc/certs/client.pem", cfg.Sinks.NATS.TLS.CertFile)
	assert.Equal(t, "/etc/certs/client-key.pem", cfg.Sinks.NATS.TLS.KeyFile)
}

// TestSinksConfig_NoSinksBlock tests that a YAML without a sinks block
// leaves Config.Sinks.NATS as nil.
func TestSinksConfig_NoSinksBlock(t *testing.T) {
	raw := `
source: "postgres://user:pass@host/db"
output: stdout
port: 7654
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	assert.Nil(t, cfg.Sinks.NATS, "NATS config should be nil when no sinks block is present")
}

// TestSinksConfig_TLSCAFileOnly tests that partial TLS config (ca-file only)
// is parsed correctly.
func TestSinksConfig_TLSCAFileOnly(t *testing.T) {
	raw := `
sinks:
  nats:
    url: "nats://localhost:4222"
    subject-template: "cdc.{{.Table}}"
    tls:
      ca-file: "/etc/ca.pem"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	require.NotNil(t, cfg.Sinks.NATS)
	assert.Equal(t, "/etc/ca.pem", cfg.Sinks.NATS.TLS.CAFile)
	assert.Equal(t, "", cfg.Sinks.NATS.TLS.CertFile, "CertFile should be empty when not set")
	assert.Equal(t, "", cfg.Sinks.NATS.TLS.KeyFile, "KeyFile should be empty when not set")
}

// TestSinksConfig_StreamNameDefaultsEmpty tests that StreamName defaults to
// empty string when not specified (it is optional).
func TestSinksConfig_StreamNameDefaultsEmpty(t *testing.T) {
	raw := `
sinks:
  nats:
    url: "nats://localhost:4222"
    subject-template: "cdc.{{.Table}}"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	require.NotNil(t, cfg.Sinks.NATS)
	assert.Equal(t, "", cfg.Sinks.NATS.StreamName, "StreamName should default to empty string")
}

// TestSinks_SQS_RoundTrip tests that a full sinks.sqs YAML block
// is parsed into Config.Sinks.SQS correctly with all fields populated.
func TestSinks_SQS_RoundTrip(t *testing.T) {
	raw := `
sinks:
  sqs:
    queue-url: "https://sqs.us-east-1.amazonaws.com/123456789/my-queue.fifo"
    region: "us-east-1"
    access-key-id: "AKID"
    secret-access-key: "SECRET"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	require.NotNil(t, cfg.Sinks.SQS, "SQS config should be non-nil when sinks.sqs block is present")
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123456789/my-queue.fifo", cfg.Sinks.SQS.QueueURL)
	assert.Equal(t, "us-east-1", cfg.Sinks.SQS.Region)
	assert.Equal(t, "AKID", cfg.Sinks.SQS.AccessKeyID)
	assert.Equal(t, "SECRET", cfg.Sinks.SQS.SecretAccessKey)
}

// TestSinks_SQS_AbsentBlock tests that when the sinks.sqs block is absent
// from YAML, cfg.Sinks.SQS is nil (not a zero-value struct).
func TestSinks_SQS_AbsentBlock(t *testing.T) {
	raw := `
sinks:
  nats:
    url: "nats://localhost:4222"
    subject-template: "cdc.{{.Table}}"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	assert.Nil(t, cfg.Sinks.SQS, "SQS config should be nil when sinks.sqs block is absent")
}

// TestSinks_SQS_TLS tests that sinks.sqs.tls.ca-file is parsed correctly.
func TestSinks_SQS_TLS(t *testing.T) {
	raw := `
sinks:
  sqs:
    queue-url: "https://sqs.us-east-1.amazonaws.com/123456789/my-queue.fifo"
    region: "us-east-1"
    tls:
      ca-file: "/etc/certs/ca.pem"
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(raw), &cfg)
	require.NoError(t, err)

	require.NotNil(t, cfg.Sinks.SQS, "SQS config should be non-nil when sinks.sqs block is present")
	assert.Equal(t, "/etc/certs/ca.pem", cfg.Sinks.SQS.TLS.CAFile)
}
