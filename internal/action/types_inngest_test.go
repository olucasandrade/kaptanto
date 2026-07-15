package action_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInngest_Name(t *testing.T) {
	it := lookupType(t, "inngest")
	assert.Equal(t, "inngest", it.Name())
}

func TestInngest_ParamSpec(t *testing.T) {
	it := lookupType(t, "inngest")
	spec := it.ParamSpec()

	ek, ok := spec["event-key"]
	require.True(t, ok, "event-key param must exist")
	assert.True(t, ek.Required, "event-key must be required")
	assert.True(t, ek.Secret, "event-key must be secret")

	ent, ok := spec["event-name-template"]
	require.True(t, ok, "event-name-template param must exist")
	assert.False(t, ent.Required, "event-name-template must be optional")
	assert.False(t, ent.Secret, "event-name-template must not be secret")
	assert.Equal(t, "kaptanto/{{.Table}}.{{.Operation}}", ent.Default)
}

func TestInngest_PinsBatch(t *testing.T) {
	it := lookupType(t, "inngest")
	assert.False(t, it.PinsBatch(), "inngest allows batching")
}

func TestInngest_ComputedAuthHeaders(t *testing.T) {
	it := lookupType(t, "inngest")
	assert.Empty(t, it.ComputedAuthHeaders(), "inngest has no computed auth headers")
}

func TestInngest_Build_DefaultTemplate(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key":           "test-key-123",
		"event-name-template": "kaptanto/{{.Table}}.{{.Operation}}",
	}

	whCfg, transform, err := it.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://inn.gs/e/test-key-123", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])

	assert.Equal(t, "jq", transform.Language)
	assert.Contains(t, transform.Expression, ".idempotency_key")
	assert.Contains(t, transform.Expression, ".table")
	assert.Contains(t, transform.Expression, ".operation")
	assert.Contains(t, transform.Expression, "now*1000")
}

func TestInngest_Build_CustomTemplate(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key":           "key-456",
		"event-name-template": "myapp/{{.Schema}}.{{.Table}}",
	}

	whCfg, transform, err := it.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://inn.gs/e/key-456", whCfg.URL)
	assert.Equal(t, "jq", transform.Language)
	assert.Contains(t, transform.Expression, ".schema")
	assert.Contains(t, transform.Expression, ".table")
}

func TestInngest_Build_StaticTemplate(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key":           "key-789",
		"event-name-template": "my-static-event",
	}

	_, transform, err := it.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "jq", transform.Language)
	assert.Contains(t, transform.Expression, `"my-static-event"`)
}

func TestInngest_Build_IdempotencyKeyMapped(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key":           "key-x",
		"event-name-template": "kaptanto/{{.Table}}.{{.Operation}}",
	}

	_, transform, err := it.Build(params)
	require.NoError(t, err)

	assert.Contains(t, transform.Expression, "id: .idempotency_key",
		"idempotency_key must be mapped to id field")
}

func TestInngest_Golden_SecretRedacted(t *testing.T) {
	t.Setenv("INNGEST_EVENT_KEY", "secret-real-key-abc")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "inngest"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "order-events",
				Type:   "inngest",
				Params: map[string]string{"event-key": "${INNGEST_EVENT_KEY}"},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:inngest:order-events", consumers[0].ID())
}

func TestInngest_Golden_LiteralSecret_Rejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(lookupType(t, "inngest"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "bad-action",
				Type:   "inngest",
				Params: map[string]string{"event-key": "literal-key-value"},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
}

func TestInngest_Golden_BatchMode_Valid(t *testing.T) {
	t.Setenv("ING_KEY", "test-key")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "inngest"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "batch-action",
				Type:   "inngest",
				Params: map[string]string{"event-key": "${ING_KEY}"},
				Batch:  &config.WebhookBatch{MaxEvents: 50},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}

// lookupType retrieves a type from the DefaultRegistry.
func lookupType(t *testing.T, name string) action.Type {
	t.Helper()
	typ := action.DefaultRegistry.Lookup(name)
	require.NotNilf(t, typ, "type %q must be registered in DefaultRegistry", name)
	return typ
}
