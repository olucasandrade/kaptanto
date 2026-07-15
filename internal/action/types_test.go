package action_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// captured holds the HTTP request fields we assert against in golden tests.
type captured struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// captureServer returns an httptest.Server that records the last request
// and responds with the given status code.
func captureServer(t *testing.T, status int) (*httptest.Server, *captured, *sync.Mutex) {
	t.Helper()
	var (
		mu  sync.Mutex
		cap captured
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		cap = captured{
			Method: r.Method,
			URL:    r.URL.String(),
			Headers: map[string]string{
				"Content-Type": r.Header.Get("Content-Type"),
			},
			Body: body,
		}
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &cap, &mu
}

// buildAndDeliver creates consumers from config, delivers an entry, and flushes.
func buildAndDeliver(t *testing.T, cfg *config.Config, entry eventlog.LogEntry) ([]router.Consumer, error) {
	t.Helper()
	reg := action.NewRegistry()
	reg.Register(action.SlackType{})
	reg.Register(action.DiscordType{})

	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	for _, c := range consumers {
		if err := c.Deliver(ctx, entry); err != nil {
			return consumers, err
		}
		if bf, ok := c.(router.BatchFlusher); ok {
			if err := bf.FlushBatch(ctx, entry.PartitionID); err != nil {
				return consumers, err
			}
		}
	}
	return consumers, nil
}

func testEntry() eventlog.LogEntry {
	ev := &event.ChangeEvent{
		Operation:      event.OpInsert,
		Schema:         "public",
		Table:          "orders",
		Key:            json.RawMessage(`{"id":42}`),
		After:          json.RawMessage(`{"id":42,"status":"paid"}`),
		IdempotencyKey: "pg:public.orders:42:insert:0/1234",
	}
	raw, _ := json.Marshal(ev)
	return eventlog.LogEntry{
		Seq:         1,
		PartitionID: 0,
		Event:       ev,
		Raw:         raw,
	}
}

// --- Slack Golden Tests ---

func TestSlackType_GoldenRequest(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("SLACK_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "order-slack",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
		}},
	}

	consumers, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:slack:order-slack", consumers[0].ID())

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "POST", cap.Method)
	assert.Equal(t, "/", cap.URL)
	assert.Equal(t, "application/json", cap.Headers["Content-Type"])

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	text, ok := body["text"].(string)
	require.True(t, ok, "expected 'text' field in Slack payload")
	assert.Contains(t, text, "[insert]")
	assert.Contains(t, text, "orders")
	assert.Contains(t, text, "key")
}

func TestSlackType_DefaultTransformShape(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("SLACK_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-shape",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
		}},
	}
	_, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	_, hasText := body["text"]
	assert.True(t, hasText, "Slack payload must have 'text' field")
	assert.Len(t, body, 1, "Slack default transform should produce only {text: ...}")
}

func TestSlackType_TransformOverride(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("SLACK_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-custom",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
			Transform: &config.TransformConfig{
				Language:   "jq",
				Expression: `{text: ("CUSTOM: " + .table)}`,
			},
		}},
	}
	_, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	assert.Equal(t, "CUSTOM: orders", body["text"])
}

func TestSlackType_BatchOverride_Rejected(t *testing.T) {
	t.Setenv("SLACK_URL", "https://hooks.slack.com/test")

	reg := action.NewRegistry()
	reg.Register(action.SlackType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-batch",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
			Batch:  &config.WebhookBatch{MaxEvents: 5},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func TestSlackType_SecretRedaction(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.SlackType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-literal",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "https://hooks.slack.com/services/LITERAL"},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "LITERAL")
}

// --- Discord Golden Tests ---

func TestDiscordType_GoldenRequest(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("DISCORD_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "order-discord",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
		}},
	}

	consumers, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:discord:order-discord", consumers[0].ID())

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "POST", cap.Method)
	assert.Equal(t, "/", cap.URL)
	assert.Equal(t, "application/json", cap.Headers["Content-Type"])

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	content, ok := body["content"].(string)
	require.True(t, ok, "expected 'content' field in Discord payload")
	assert.Contains(t, content, "[insert]")
	assert.Contains(t, content, "orders")
	assert.Contains(t, content, "key")
}

func TestDiscordType_DefaultTransformShape(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("DISCORD_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-shape",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
		}},
	}
	_, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	_, hasContent := body["content"]
	assert.True(t, hasContent, "Discord payload must have 'content' field")
	assert.Len(t, body, 1, "Discord default transform should produce only {content: ...}")
}

func TestDiscordType_TransformOverride(t *testing.T) {
	srv, cap, mu := captureServer(t, http.StatusOK)
	t.Setenv("DISCORD_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-custom",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
			Transform: &config.TransformConfig{
				Language:   "jq",
				Expression: `{content: ("CUSTOM: " + .table)}`,
			},
		}},
	}
	_, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.Body, &body))
	assert.Equal(t, "CUSTOM: orders", body["content"])
}

func TestDiscordType_BatchOverride_Rejected(t *testing.T) {
	t.Setenv("DISCORD_URL", "https://discord.com/api/webhooks/test")

	reg := action.NewRegistry()
	reg.Register(action.DiscordType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-batch",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
			Batch:  &config.WebhookBatch{MaxEvents: 10},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func TestDiscordType_SecretRedaction(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(action.DiscordType{})

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-literal",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "https://discord.com/api/webhooks/LITERAL"},
		}},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "LITERAL")
}

// --- Response Classification Tests (429 / 404) ---

func TestSlackType_429_Transient(t *testing.T) {
	srv, _, _ := captureServer(t, http.StatusTooManyRequests)
	t.Setenv("SLACK_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-429",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
		}},
	}

	_, err := buildAndDeliver(t, cfg, testEntry())
	require.Error(t, err)

	var permErr *router.PermanentFlushError
	assert.False(t, errors.As(err, &permErr), "429 must be transient, not permanent")
}

func TestSlackType_404_Poison(t *testing.T) {
	srv, _, _ := captureServer(t, http.StatusNotFound)
	t.Setenv("SLACK_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "slack-404",
			Type:   "slack",
			Params: map[string]string{"webhook-url": "${SLACK_URL}"},
		}},
	}

	_, err := buildAndDeliver(t, cfg, testEntry())
	require.Error(t, err)

	var permErr *router.PermanentFlushError
	assert.True(t, errors.As(err, &permErr), "404 must be permanent (poison)")
}

func TestDiscordType_429_Transient(t *testing.T) {
	srv, _, _ := captureServer(t, http.StatusTooManyRequests)
	t.Setenv("DISCORD_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-429",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
		}},
	}

	_, err := buildAndDeliver(t, cfg, testEntry())
	require.Error(t, err)

	var permErr *router.PermanentFlushError
	assert.False(t, errors.As(err, &permErr), "429 must be transient, not permanent")
}

func TestDiscordType_404_Poison(t *testing.T) {
	srv, _, _ := captureServer(t, http.StatusNotFound)
	t.Setenv("DISCORD_URL", srv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{{
			Name:   "discord-404",
			Type:   "discord",
			Params: map[string]string{"webhook-url": "${DISCORD_URL}"},
		}},
	}

	_, err := buildAndDeliver(t, cfg, testEntry())
	require.Error(t, err)

	var permErr *router.PermanentFlushError
	assert.True(t, errors.As(err, &permErr), "404 must be permanent (poison)")
}

// --- Registry integration ---

func TestSlackType_RegisteredInDefaultRegistry(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("slack")
	require.NotNil(t, typ)
	assert.Equal(t, "slack", typ.Name())
	assert.True(t, typ.PinsBatch())
	assert.Nil(t, typ.ComputedAuthHeaders())

	spec := typ.ParamSpec()
	require.Contains(t, spec, "webhook-url")
	assert.True(t, spec["webhook-url"].Required)
	assert.True(t, spec["webhook-url"].Secret)
}

func TestDiscordType_RegisteredInDefaultRegistry(t *testing.T) {
	typ := action.DefaultRegistry.Lookup("discord")
	require.NotNil(t, typ)
	assert.Equal(t, "discord", typ.Name())
	assert.True(t, typ.PinsBatch())
	assert.Nil(t, typ.ComputedAuthHeaders())

	spec := typ.ParamSpec()
	require.Contains(t, spec, "webhook-url")
	assert.True(t, spec["webhook-url"].Required)
	assert.True(t, spec["webhook-url"].Secret)
}

// --- Build produces valid config ---

func TestSlackType_Build_ValidConfig(t *testing.T) {
	var st action.SlackType
	whCfg, tfCfg, err := st.Build(action.ResolvedParams{
		"webhook-url": "https://hooks.slack.com/services/T/B/xxx",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.slack.com/services/T/B/xxx", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])
	assert.Equal(t, "jq", tfCfg.Language)
	assert.NotEmpty(t, tfCfg.Expression)
}

func TestDiscordType_Build_ValidConfig(t *testing.T) {
	var dt action.DiscordType
	whCfg, tfCfg, err := dt.Build(action.ResolvedParams{
		"webhook-url": "https://discord.com/api/webhooks/123/abc",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://discord.com/api/webhooks/123/abc", whCfg.URL)
	assert.Equal(t, "POST", whCfg.Method)
	assert.Equal(t, 1, whCfg.Batch.MaxEvents)
	assert.Equal(t, "application/json", whCfg.Headers["Content-Type"])
	assert.Equal(t, "jq", tfCfg.Language)
	assert.NotEmpty(t, tfCfg.Expression)
}

// --- Both types side by side ---

func TestSlackAndDiscord_SideBySide(t *testing.T) {
	slackSrv, slackCap, slackMu := captureServer(t, http.StatusOK)
	discordSrv, discordCap, discordMu := captureServer(t, http.StatusOK)
	t.Setenv("SLACK_URL", slackSrv.URL)
	t.Setenv("DISCORD_URL", discordSrv.URL)

	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "slack-notify", Type: "slack", Params: map[string]string{"webhook-url": "${SLACK_URL}"}},
			{Name: "discord-notify", Type: "discord", Params: map[string]string{"webhook-url": "${DISCORD_URL}"}},
		},
	}

	consumers, err := buildAndDeliver(t, cfg, testEntry())
	require.NoError(t, err)
	require.Len(t, consumers, 2)

	slackMu.Lock()
	var slackBody map[string]any
	require.NoError(t, json.Unmarshal(slackCap.Body, &slackBody))
	_, hasText := slackBody["text"]
	assert.True(t, hasText, "Slack payload must have 'text'")
	slackMu.Unlock()

	discordMu.Lock()
	var discordBody map[string]any
	require.NoError(t, json.Unmarshal(discordCap.Body, &discordBody))
	_, hasContent := discordBody["content"]
	assert.True(t, hasContent, "Discord payload must have 'content'")
	discordMu.Unlock()
}

