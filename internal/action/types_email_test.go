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
	"github.com/olucasandrade/kaptanto/internal/transform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendgridEnvelope mirrors the SendGrid v3 mail/send JSON shape for
// assertion convenience. Kept in test to avoid coupling prod code
// to a SendGrid-specific struct.
type sendgridEnvelope struct {
	Personalizations []struct {
		To []struct {
			Email string `json:"email"`
		} `json:"to"`
	} `json:"personalizations"`
	From struct {
		Email string `json:"email"`
	} `json:"from"`
	Subject string `json:"subject"`
	Content []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"content"`
}

func sampleEvent() (*event.ChangeEvent, []byte) {
	ev := &event.ChangeEvent{
		ID:             ulid.MustParse("01HYQXJKZ00000000000000000"),
		IdempotencyKey: "default:public.orders:42:insert:0/1",
		Timestamp:      time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Source:         "default",
		Operation:      event.OpInsert,
		Schema:         "public",
		Table:          "orders",
		Key:            json.RawMessage(`{"id":42}`),
		Before:         nil,
		After:          json.RawMessage(`{"id":42,"status":"new","total":99.95}`),
	}
	raw, _ := json.Marshal(ev)
	return ev, raw
}

// --- Golden request test: frozen SendGrid v3 envelope ---

func TestEmailType_GoldenEnvelope(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.frozen-test-key",
		"from":             "alerts@example.com",
		"to":               "ops@example.com",
		"subject-template": "[kaptanto] {{.Operation}} on {{.Table}}",
	}

	whCfg, transformCfg, err := action.EmailType{}.Build(params)
	require.NoError(t, err)

	// --- Webhook config assertions ---
	assert.Equal(t, "https://api.sendgrid.com/v3/mail/send", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)

	// Auth header asserted
	assert.Equal(t, "Bearer SG.frozen-test-key", whCfg.Headers["Authorization"])
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])

	// --- Transform produces valid v3 envelope ---
	assert.Equal(t, "jq", transformCfg.Language)

	engine, err := transform.Compile(transformCfg.Language, transformCfg.Expression)
	require.NoError(t, err)

	ev, raw := sampleEvent()
	out, drop, err := engine.Apply(raw, ev)
	require.NoError(t, err)
	assert.False(t, drop)

	var envelope sendgridEnvelope
	require.NoError(t, json.Unmarshal(out, &envelope))

	// personalizations/to
	require.Len(t, envelope.Personalizations, 1)
	require.Len(t, envelope.Personalizations[0].To, 1)
	assert.Equal(t, "ops@example.com", envelope.Personalizations[0].To[0].Email)

	// from
	assert.Equal(t, "alerts@example.com", envelope.From.Email)

	// subject rendered from template
	assert.Equal(t, "[kaptanto] insert on orders", envelope.Subject)

	// content[0] = text/plain JSON dump
	require.Len(t, envelope.Content, 1)
	assert.Equal(t, "text/plain", envelope.Content[0].Type)
	assert.Contains(t, envelope.Content[0].Value, `"operation":"insert"`)
	assert.Contains(t, envelope.Content[0].Value, `"table":"orders"`)
	assert.Contains(t, envelope.Content[0].Value, `"id":42`)
}

// --- Auth header and secret redaction ---

func TestEmailType_AuthHeader_BearerToken(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.my-secret-key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	whCfg, _, err := action.EmailType{}.Build(params)
	require.NoError(t, err)

	assert.Equal(t, "Bearer SG.my-secret-key", whCfg.Headers["Authorization"])
}

func TestEmailType_SecretRedacted_APIKey(t *testing.T) {
	spec := action.EmailType{}.ParamSpec()
	assert.True(t, spec["api-key"].Secret, "api-key must be marked as secret for ACT-02 redaction")
	assert.True(t, spec["api-key"].Required, "api-key must be required")
}

func TestEmailType_SecretPolicy_LiteralAPIKey_Rejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "SG.literal-secret",
				"from":    "a@b.com",
				"to":      "c@d.com",
			},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "api-key")
}

// --- Provider validation ---

func TestEmailType_ProviderSMTP_Error(t *testing.T) {
	params := action.ResolvedParams{
		"provider":         "smtp",
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	_, _, err := action.EmailType{}.Build(params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smtp")
	assert.Contains(t, err.Error(), "not supported")
}

func TestEmailType_ProviderSMTP_CaseInsensitive(t *testing.T) {
	params := action.ResolvedParams{
		"provider":         "SMTP",
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	_, _, err := action.EmailType{}.Build(params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestEmailType_ProviderUnknown_Error(t *testing.T) {
	params := action.ResolvedParams{
		"provider":         "mailgun",
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	_, _, err := action.EmailType{}.Build(params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider")
}

func TestEmailType_ProviderSendgrid_Accepted(t *testing.T) {
	params := action.ResolvedParams{
		"provider":         "sendgrid",
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	_, _, err := action.EmailType{}.Build(params)
	require.NoError(t, err)
}

func TestEmailType_ProviderEmpty_Accepted(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "test",
	}
	_, _, err := action.EmailType{}.Build(params)
	require.NoError(t, err)
}

// --- Missing from/to → startup error ---

func TestEmailType_MissingFrom_StartupError(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.test")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"to":      "c@d.com",
			},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required param")
	assert.Contains(t, err.Error(), "from")
}

func TestEmailType_MissingTo_StartupError(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.test")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"from":    "a@b.com",
			},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required param")
	assert.Contains(t, err.Error(), "to")
}

// --- Batching override rejected ---

func TestEmailType_BatchOverride_Rejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.test")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"from":    "a@b.com",
				"to":      "c@d.com",
			},
			Batch: &config.WebhookBatch{MaxEvents: 10},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

// --- Authorization header collision rejected ---

func TestEmailType_AuthHeaderCollision_Rejected(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.test")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"from":    "a@b.com",
				"to":      "c@d.com",
			},
			Headers: map[string]string{
				"Authorization": "Bearer override-attempt",
			},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with type's computed auth header")
}

// --- Subject template default and override ---

func TestEmailType_SubjectTemplate_Default(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.test")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "email-alert",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"from":    "a@b.com",
				"to":      "c@d.com",
			},
		}},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
}

func TestEmailType_SubjectTemplate_CustomOverride(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "Alert: {{.Operation}} on {{.Schema}}.{{.Table}}",
	}

	_, transformCfg, err := action.EmailType{}.Build(params)
	require.NoError(t, err)

	engine, err := transform.Compile(transformCfg.Language, transformCfg.Expression)
	require.NoError(t, err)

	ev, raw := sampleEvent()
	out, drop, err := engine.Apply(raw, ev)
	require.NoError(t, err)
	assert.False(t, drop)

	var envelope sendgridEnvelope
	require.NoError(t, json.Unmarshal(out, &envelope))

	assert.Equal(t, "Alert: insert on public.orders", envelope.Subject)
}

func TestEmailType_SubjectTemplate_PlainString(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "CDC notification",
	}

	_, transformCfg, err := action.EmailType{}.Build(params)
	require.NoError(t, err)

	engine, err := transform.Compile(transformCfg.Language, transformCfg.Expression)
	require.NoError(t, err)

	ev, raw := sampleEvent()
	out, drop, err := engine.Apply(raw, ev)
	require.NoError(t, err)
	assert.False(t, drop)

	var envelope sendgridEnvelope
	require.NoError(t, json.Unmarshal(out, &envelope))

	assert.Equal(t, "CDC notification", envelope.Subject)
}

func TestEmailType_SubjectTemplate_UnsupportedField(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "{{.Bogus}} event",
	}

	_, _, err := action.EmailType{}.Build(params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported field")
	assert.Contains(t, err.Error(), "Bogus")
}

// --- End-to-end BuildConsumers success ---

func TestEmailType_BuildConsumers_Success(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.EmailType{})
	t.Setenv("SG_KEY", "SG.production-key")

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name: "order-emails",
			Type: "email",
			Params: map[string]string{
				"api-key": "${SG_KEY}",
				"from":    "cdc@myapp.com",
				"to":      "team@myapp.com",
			},
			Match: config.MatchConfig{
				Tables:     []string{"public.orders"},
				Operations: []string{"insert", "delete"},
			},
		}},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:email:order-emails", consumers[0].ID())
}

// --- Name and basic interface ---

func TestEmailType_Name(t *testing.T) {
	assert.Equal(t, "email", action.EmailType{}.Name())
}

func TestEmailType_PinsBatch(t *testing.T) {
	assert.True(t, action.EmailType{}.PinsBatch(), "email must pin batch.max-events to 1")
}

func TestEmailType_ComputedAuthHeaders(t *testing.T) {
	headers := action.EmailType{}.ComputedAuthHeaders()
	assert.Equal(t, []string{"Authorization"}, headers)
}

func TestEmailType_ParamSpec_AllDeclared(t *testing.T) {
	spec := action.EmailType{}.ParamSpec()
	assert.Contains(t, spec, "api-key")
	assert.Contains(t, spec, "from")
	assert.Contains(t, spec, "to")
	assert.Contains(t, spec, "provider")
	assert.Contains(t, spec, "subject-template")
	assert.Len(t, spec, 5)
}

// --- Update event operation ---

func TestEmailType_EnvelopeWithUpdateOperation(t *testing.T) {
	params := action.ResolvedParams{
		"api-key":          "SG.key",
		"from":             "a@b.com",
		"to":               "c@d.com",
		"subject-template": "[kaptanto] {{.Operation}} on {{.Table}}",
	}

	_, transformCfg, err := action.EmailType{}.Build(params)
	require.NoError(t, err)

	engine, err := transform.Compile(transformCfg.Language, transformCfg.Expression)
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		Operation: event.OpUpdate,
		Schema:    "public",
		Table:     "users",
		Key:       json.RawMessage(`{"id":7}`),
		Before:    json.RawMessage(`{"id":7,"name":"old"}`),
		After:     json.RawMessage(`{"id":7,"name":"new"}`),
		Timestamp: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, _ := json.Marshal(ev)

	out, drop, err := engine.Apply(raw, ev)
	require.NoError(t, err)
	assert.False(t, drop)

	var envelope sendgridEnvelope
	require.NoError(t, json.Unmarshal(out, &envelope))

	assert.Equal(t, "[kaptanto] update on users", envelope.Subject)
	assert.Contains(t, envelope.Content[0].Value, `"operation":"update"`)
}
