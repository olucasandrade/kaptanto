package action_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTriggerdev_Name(t *testing.T) {
	td := lookupType(t, "triggerdev")
	assert.Equal(t, "triggerdev", td.Name())
}

func TestTriggerdev_ParamSpec(t *testing.T) {
	td := lookupType(t, "triggerdev")
	spec := td.ParamSpec()

	ak, ok := spec["api-key"]
	require.True(t, ok, "api-key param must exist")
	assert.True(t, ak.Required, "api-key must be required")
	assert.True(t, ak.Secret, "api-key must be secret")

	au, ok := spec["api-url"]
	require.True(t, ok, "api-url param must exist")
	assert.False(t, au.Required, "api-url must be optional")
	assert.False(t, au.Secret, "api-url must not be secret")
	assert.Equal(t, "https://api.trigger.dev", au.Default)

	ent, ok := spec["event-name-template"]
	require.True(t, ok, "event-name-template param must exist")
	assert.False(t, ent.Required, "event-name-template must be optional")
	assert.Equal(t, "kaptanto/{{.Table}}.{{.Operation}}", ent.Default)
}

func TestTriggerdev_PinsBatch(t *testing.T) {
	td := lookupType(t, "triggerdev")
	assert.True(t, td.PinsBatch(), "triggerdev must pin batch to 1")
}

func TestTriggerdev_ComputedAuthHeaders(t *testing.T) {
	td := lookupType(t, "triggerdev")
	assert.Equal(t, []string{"Authorization"}, td.ComputedAuthHeaders())
}

func TestTriggerdev_Build_DefaultURL(t *testing.T) {
	td := lookupType(t, "triggerdev")
	params := action.ResolvedParams{
		"api-key":             "tr_test_abc123",
		"api-url":             "https://api.trigger.dev",
		"event-name-template": "kaptanto/{{.Table}}.{{.Operation}}",
	}

	whCfg, transform, err := td.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://api.trigger.dev/api/v1/events", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, "Bearer tr_test_abc123", whCfg.Headers["Authorization"])
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)

	assert.Equal(t, "jq", transform.Language)
	assert.Contains(t, transform.Expression, ".idempotency_key")
	assert.Contains(t, transform.Expression, "payload")
	assert.Contains(t, transform.Expression, ".table")
	assert.Contains(t, transform.Expression, ".operation")
}

func TestTriggerdev_Build_CustomURL(t *testing.T) {
	td := lookupType(t, "triggerdev")
	params := action.ResolvedParams{
		"api-key":             "tr_test_key",
		"api-url":             "https://my.trigger.dev",
		"event-name-template": "kaptanto/{{.Table}}.{{.Operation}}",
	}

	whCfg, _, err := td.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://my.trigger.dev/api/v1/events", whCfg.URL)
}

func TestTriggerdev_Build_CustomURL_TrailingSlash(t *testing.T) {
	td := lookupType(t, "triggerdev")
	params := action.ResolvedParams{
		"api-key":             "tr_test_key",
		"api-url":             "https://my.trigger.dev/",
		"event-name-template": "kaptanto/{{.Table}}.{{.Operation}}",
	}

	whCfg, _, err := td.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://my.trigger.dev/api/v1/events", whCfg.URL,
		"trailing slash on api-url must be stripped")
}

func TestTriggerdev_Build_CustomTemplate(t *testing.T) {
	td := lookupType(t, "triggerdev")
	params := action.ResolvedParams{
		"api-key":             "tr_test_key",
		"api-url":             "https://api.trigger.dev",
		"event-name-template": "myapp/{{.Schema}}.{{.Table}}",
	}

	_, transform, err := td.Build(params)
	require.NoError(t, err)

	assert.Contains(t, transform.Expression, ".schema")
	assert.Contains(t, transform.Expression, ".table")
}

func TestTriggerdev_Golden_SecretRedacted(t *testing.T) {
	t.Setenv("TRIGGER_API_KEY", "tr_secret_real_key")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "triggerdev"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "order-sync",
				Type:   "triggerdev",
				Params: map[string]string{"api-key": "${TRIGGER_API_KEY}"},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:triggerdev:order-sync", consumers[0].ID())
}

func TestTriggerdev_Golden_LiteralSecret_Rejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(lookupType(t, "triggerdev"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "bad-action",
				Type:   "triggerdev",
				Params: map[string]string{"api-key": "literal-api-key"},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
}

func TestTriggerdev_Golden_BatchOverride_Rejected(t *testing.T) {
	t.Setenv("TRIGGER_API_KEY", "tr_test_key")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "triggerdev"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "batch-override",
				Type:   "triggerdev",
				Params: map[string]string{"api-key": "${TRIGGER_API_KEY}"},
				Batch:  &config.WebhookBatch{MaxEvents: 10},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func TestTriggerdev_Golden_APIURLOverride_Honored(t *testing.T) {
	t.Setenv("TRIGGER_API_KEY", "tr_test_key")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "triggerdev"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name: "custom-url",
				Type: "triggerdev",
				Params: map[string]string{
					"api-key": "${TRIGGER_API_KEY}",
					"api-url": "https://self-hosted.example.com",
				},
			},
		},
	}

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}

func TestTriggerdev_Golden_AuthHeaderCollision_Rejected(t *testing.T) {
	t.Setenv("TRIGGER_API_KEY", "tr_test_key")

	reg := action.NewRegistry()
	reg.Register(lookupType(t, "triggerdev"))

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:    "header-collision",
				Type:    "triggerdev",
				Params:  map[string]string{"api-key": "${TRIGGER_API_KEY}"},
				Headers: map[string]string{"authorization": "Bearer override"},
			},
		},
	}

	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with type's computed auth header")
}
