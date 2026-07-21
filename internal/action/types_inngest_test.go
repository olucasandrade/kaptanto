package action_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/observability"
	tform "github.com/olucasandrade/kaptanto/internal/transform"
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

	whCfg, tc, err := it.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "https://inn.gs/e/test-key-123", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])

	assert.Equal(t, "jq", tc.Language)
	assert.Contains(t, tc.Expression, ".idempotency_key")
	assert.Contains(t, tc.Expression, ".table")
	assert.Contains(t, tc.Expression, ".operation")
	assert.Contains(t, tc.Expression, "fromdateiso8601")

	// Compile and execute the transform against a real event to catch invalid
	// jq runtime behavior such as applying floor to a serialized timestamp.
	eng, err := tform.Compile(tc.Language, tc.Expression)
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		ID:             ulid.MustParse("01J0000000000000000000000A"),
		IdempotencyKey: "pg:public.orders:1:insert:0/1",
		Timestamp:      time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Source:         "postgres",
		Operation:      event.OpInsert,
		Database:       "app",
		Schema:         "public",
		Table:          "orders",
		Key:            json.RawMessage(`{"id":1}`),
		After:          json.RawMessage(`{"id":1,"status":"new"}`),
		Metadata:       map[string]any{"lsn": "0/1"},
	}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)

	out, drop, err := eng.Apply(raw, ev)
	require.NoError(t, err)
	require.False(t, drop)
	require.NotNil(t, out)

	var envelope struct {
		Name string                 `json:"name"`
		ID   string                 `json:"id"`
		TS   float64                `json:"ts"`
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &envelope))
	assert.Equal(t, "kaptanto/orders.insert", envelope.Name)
	assert.Equal(t, ev.IdempotencyKey, envelope.ID)
	assert.Equal(t, "orders", envelope.Data["table"])

	// ts must be derived from the event's RFC3339 timestamp, not from "now".
	wantTS := float64(ev.Timestamp.UnixMilli())
	assert.InDelta(t, wantTS, envelope.TS, 1.0, "ts should be event time in milliseconds")
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

func TestInngest_Build_UnsupportedTemplateSyntax(t *testing.T) {
	it := lookupType(t, "inngest")

	unsupported := []string{
		`myapp/{{.Table | upper}}`,
		`{{if .Table}}x{{end}}`,
		`{{.UnknownField}}`,
	}

	for _, tmpl := range unsupported {
		_, _, err := it.Build(action.ResolvedParams{
			"event-key":           "key",
			"event-name-template": tmpl,
		})
		require.Error(t, err, "template %q must be rejected", tmpl)
	}
}

func TestInngest_Build_WhitespaceTemplate(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key":           "key",
		"event-name-template": "kaptanto/{{ .Table }}.{{ .Operation }}",
	}

	_, tc, err := it.Build(params)
	require.NoError(t, err)
	assert.Contains(t, tc.Expression, ".table")
	assert.Contains(t, tc.Expression, ".operation")
	assert.NotContains(t, tc.Expression, "{{")
}

func TestInngest_Build_APIURLHonored(t *testing.T) {
	it := lookupType(t, "inngest")
	params := action.ResolvedParams{
		"event-key": "key",
		"api-url":   "http://inngest:8288",
	}

	whCfg, _, err := it.Build(params)
	require.NoError(t, err)
	assert.Equal(t, "http://inngest:8288/e/key", whCfg.URL)
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
