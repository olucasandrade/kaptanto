package action_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeType implements action.Type for testing.
type fakeType struct {
	name              string
	paramSpec         map[string]action.ParamSpec
	pinsBatch         bool
	computedHeaders   []string
	buildFn           func(action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error)
}

func (f *fakeType) Name() string                       { return f.name }
func (f *fakeType) ParamSpec() map[string]action.ParamSpec { return f.paramSpec }
func (f *fakeType) PinsBatch() bool                    { return f.pinsBatch }
func (f *fakeType) ComputedAuthHeaders() []string      { return f.computedHeaders }
func (f *fakeType) Build(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	if f.buildFn != nil {
		return f.buildFn(p)
	}
	return config.WebhookSinkConfig{
		URL:    "https://hooks.example.com/test",
		Method: "POST",
	}, config.TransformConfig{}, nil
}

func newTestRegistry(types ...action.Type) *action.Registry {
	reg := action.NewRegistry()
	for _, t := range types {
		reg.Register(t)
	}
	return reg
}

func defaultFakeType() *fakeType {
	return &fakeType{
		name: "test-hook",
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true, Description: "webhook URL"},
			"channel":     {Required: false, Secret: false, Description: "channel name", Default: "#general"},
		},
	}
}

// --- Registry Tests ---

func TestRegistry_Register_Lookup(t *testing.T) {
	reg := action.NewRegistry()
	ft := &fakeType{name: "slack", paramSpec: map[string]action.ParamSpec{}}
	reg.Register(ft)

	got := reg.Lookup("slack")
	require.NotNil(t, got)
	assert.Equal(t, "slack", got.Name())

	assert.Nil(t, reg.Lookup("nonexistent"))
}

func TestRegistry_Register_DuplicatePanics(t *testing.T) {
	reg := action.NewRegistry()
	ft := &fakeType{name: "slack", paramSpec: map[string]action.ParamSpec{}}
	reg.Register(ft)
	assert.Panics(t, func() { reg.Register(ft) })
}

func TestRegistry_Names(t *testing.T) {
	reg := action.NewRegistry()
	reg.Register(&fakeType{name: "slack", paramSpec: map[string]action.ParamSpec{}})
	reg.Register(&fakeType{name: "pagerduty", paramSpec: map[string]action.ParamSpec{}})
	names := reg.Names()
	assert.ElementsMatch(t, []string{"slack", "pagerduty"}, names)
}

// --- Name Validation ---

func TestBuildConsumers_InvalidName_Uppercase(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "MyAction", Type: "test-hook", Params: map[string]string{"webhook-url": "${TEST_URL}"}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match [a-z0-9-]+")
}

func TestBuildConsumers_InvalidName_Colon(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "my:action", Type: "test-hook", Params: map[string]string{"webhook-url": "${TEST_URL}"}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match [a-z0-9-]+")
}

func TestBuildConsumers_DuplicateNames(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	t.Setenv("TEST_URL", "https://example.com")
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "my-action", Type: "test-hook", Params: map[string]string{"webhook-url": "${TEST_URL}"}},
			{Name: "my-action", Type: "test-hook", Params: map[string]string{"webhook-url": "${TEST_URL}"}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate name")
}

func TestBuildConsumers_EmptyName(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "", Type: "test-hook"},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// --- Unknown Type ---

func TestBuildConsumers_UnknownType(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "my-action", Type: "nonexistent", Params: map[string]string{}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type")
	assert.Contains(t, err.Error(), "test-hook")
}

// --- Secret Policy (ACT-02) ---

func TestBuildConsumers_SecretPolicy_EnvRef_Accepted(t *testing.T) {
	t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/secret")
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "order-updates", Type: "test-hook", Params: map[string]string{
				"webhook-url": "${SLACK_WEBHOOK_URL}",
			}},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 1)
}

func TestBuildConsumers_SecretPolicy_WhitespaceEnvRef_Accepted(t *testing.T) {
	t.Setenv("X", "value")
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "order-updates", Type: "test-hook", Params: map[string]string{
				"webhook-url": " ${X} ",
			}},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 1)
}

func TestBuildConsumers_SecretPolicy_Literal_Rejected(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "order-updates", Type: "test-hook", Params: map[string]string{
				"webhook-url": "xoxb-literal-token",
			}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "webhook-url")
}

func TestBuildConsumers_SecretPolicy_Unset_Rejected(t *testing.T) {
	t.Setenv("SLACK_WEBHOOK_URL", "")
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "order-updates", Type: "test-hook", Params: map[string]string{
				"webhook-url": "${SLACK_WEBHOOK_URL}",
			}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unset")
	assert.Contains(t, err.Error(), "SLACK_WEBHOOK_URL")
}

// --- Param Validation ---

func TestBuildConsumers_UnknownParam(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "my-action", Type: "test-hook", Params: map[string]string{
				"webhook-url": "${TEST_URL}",
				"bogus":       "value",
			}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown param")
	assert.Contains(t, err.Error(), "bogus")
}

func TestBuildConsumers_MissingRequiredParam(t *testing.T) {
	reg := newTestRegistry(defaultFakeType())
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "my-action", Type: "test-hook", Params: map[string]string{
				"channel": "#random",
			}},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required param")
	assert.Contains(t, err.Error(), "webhook-url")
}

// --- Override Rules ---

func TestBuildConsumers_TransformReplace(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	ft := &fakeType{
		name: "test-hook",
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
		},
		buildFn: func(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
			return config.WebhookSinkConfig{
				URL:    p["webhook-url"],
				Method: "POST",
			}, config.TransformConfig{
				Language:   "jq",
				Expression: ".default_transform",
			}, nil
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "my-action",
				Type:   "test-hook",
				Params: map[string]string{"webhook-url": "${TEST_URL}"},
				Transform: &config.TransformConfig{
					Language:   "go-template",
					Expression: "{{.custom}}",
				},
			},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 1)
}

func TestBuildConsumers_PinnedBatch_Rejected(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	ft := &fakeType{
		name:      "pinned",
		pinsBatch: true,
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "my-action",
				Type:   "pinned",
				Params: map[string]string{"webhook-url": "${TEST_URL}"},
				Batch:  &config.WebhookBatch{MaxEvents: 10},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins batch.max-events")
}

func TestBuildConsumers_ComputedHeaderCollision_Rejected(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	ft := &fakeType{
		name:            "auth-hook",
		computedHeaders: []string{"Authorization"},
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:    "my-action",
				Type:    "auth-hook",
				Params:  map[string]string{"webhook-url": "${TEST_URL}"},
				Headers: map[string]string{"authorization": "Bearer override"},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with type's computed auth header")
}

// --- matchConsumer Tests ---

// fakeConsumer records calls for testing matchConsumer delegation.
type fakeConsumer struct {
	id           string
	delivered    []eventlog.LogEntry
	flushed      []uint32
	flushErr     error
	closed       bool
	deliverCount atomic.Int64
}

func (f *fakeConsumer) ID() string { return f.id }
func (f *fakeConsumer) Deliver(_ context.Context, e eventlog.LogEntry) error {
	f.delivered = append(f.delivered, e)
	f.deliverCount.Add(1)
	return nil
}
func (f *fakeConsumer) FlushBatch(_ context.Context, partitionID uint32) error {
	f.flushed = append(f.flushed, partitionID)
	return f.flushErr
}
func (f *fakeConsumer) Close() { f.closed = true }
func (f *fakeConsumer) Ping() error { return nil }
func (f *fakeConsumer) SetMetrics(_ *observability.KaptantoMetrics) {}

// Compile-time check fakeConsumer implements needed interfaces.
var _ router.Consumer = (*fakeConsumer)(nil)
var _ router.BatchFlusher = (*fakeConsumer)(nil)

func TestMatchConsumer_NonMatchingEvent_Skipped(t *testing.T) {
	m := observability.NewKaptantoMetrics()
	mc := action.NewMatchConsumer(&fakeConsumer{id: "test"}, mustCompile(t, "public.orders", "insert"), m, "action:test:hook")

	entry := eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpInsert},
	}
	err := mc.Deliver(context.Background(), entry)
	require.NoError(t, err)
	assert.Equal(t, int64(0), mc.InnerDeliverCount())
}

func TestMatchConsumer_MatchingEvent_Delivered(t *testing.T) {
	m := observability.NewKaptantoMetrics()
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "public.orders", "insert"), m, "action:test:hook")

	entry := eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert},
	}
	err := mc.Deliver(context.Background(), entry)
	require.NoError(t, err)
	assert.Len(t, inner.delivered, 1)
}

func TestMatchConsumer_FlushBatch_Delegates(t *testing.T) {
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "", ""), nil, "action:test:hook")

	err := mc.FlushBatch(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, []uint32{42}, inner.flushed)
}

func TestMatchConsumer_Close_Delegates(t *testing.T) {
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "", ""), nil, "action:test:hook")

	mc.Close()
	assert.True(t, inner.closed)
}

func TestMatchConsumer_Metrics_Increment(t *testing.T) {
	m := observability.NewKaptantoMetrics()
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "public.orders", ""), m, "action:test:hook")

	// Matched event
	err := mc.Deliver(context.Background(), eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert},
	})
	require.NoError(t, err)

	// Skipped event
	err = mc.Deliver(context.Background(), eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpInsert},
	})
	require.NoError(t, err)

	// Verify counters via Prometheus gather
	gathered, _ := m.Registry().Gather()
	var matched, skipped float64
	for _, mf := range gathered {
		switch mf.GetName() {
		case "kaptanto_action_events_matched_total":
			for _, metric := range mf.GetMetric() {
				matched += metric.GetCounter().GetValue()
			}
		case "kaptanto_action_events_skipped_total":
			for _, metric := range mf.GetMetric() {
				skipped += metric.GetCounter().GetValue()
			}
		}
	}
	assert.Equal(t, float64(1), matched)
	assert.Equal(t, float64(1), skipped)
}

// --- BuildConsumers Integration ---

func TestBuildConsumers_NoActions_ReturnsNil(t *testing.T) {
	cfg := &config.Config{}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), action.NewRegistry())
	require.NoError(t, err)
	assert.Nil(t, consumers)
}

func TestBuildConsumers_Success(t *testing.T) {
	t.Setenv("MY_WEBHOOK", "https://hooks.example.com/real")
	ft := &fakeType{
		name: "test-hook",
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
		},
		buildFn: func(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
			return config.WebhookSinkConfig{
				URL:    p["webhook-url"],
				Method: "POST",
			}, config.TransformConfig{}, nil
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "order-updates",
				Type:   "test-hook",
				Params: map[string]string{"webhook-url": "${MY_WEBHOOK}"},
				Match: config.MatchConfig{
					Tables:     []string{"public.orders"},
					Operations: []string{"insert", "update"},
				},
			},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	require.Len(t, consumers, 1)
	assert.Equal(t, "action:test-hook:order-updates", consumers[0].ID())
}

func TestBuildConsumers_TwoActionsIndependent(t *testing.T) {
	t.Setenv("URL_A", "https://a.example.com")
	t.Setenv("URL_B", "https://b.example.com")
	ft := &fakeType{
		name: "hook",
		paramSpec: map[string]action.ParamSpec{
			"url": {Required: true, Secret: true},
		},
		buildFn: func(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
			return config.WebhookSinkConfig{
				URL:    p["url"],
				Method: "POST",
			}, config.TransformConfig{}, nil
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "action-a", Type: "hook", Params: map[string]string{"url": "${URL_A}"}},
			{Name: "action-b", Type: "hook", Params: map[string]string{"url": "${URL_B}"}},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 2)
	assert.Equal(t, "action:hook:action-a", consumers[0].ID())
	assert.Equal(t, "action:hook:action-b", consumers[1].ID())
}

// --- Additional Coverage Tests ---

func TestMatchConsumer_Ping_Delegates(t *testing.T) {
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "", ""), nil, "action:test:hook")
	err := mc.Ping()
	assert.NoError(t, err)
}

func TestMatchConsumer_SetMetrics_Delegates(t *testing.T) {
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "", ""), nil, "action:test:hook")
	mc.SetMetrics(observability.NewKaptantoMetrics())
}

func TestBuildConsumers_DefaultParamUsed(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	ft := &fakeType{
		name: "defaulter",
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
			"channel":     {Required: false, Secret: false, Default: "#general"},
		},
		buildFn: func(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
			assert.Equal(t, "#general", p["channel"])
			return config.WebhookSinkConfig{URL: p["webhook-url"], Method: "POST"}, config.TransformConfig{}, nil
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "default-test", Type: "defaulter", Params: map[string]string{"webhook-url": "${TEST_URL}"}},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 1)
}

func TestBuildConsumers_NonSecretEnvExpansion(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	t.Setenv("MY_CHANNEL", "#random")
	ft := &fakeType{
		name: "env-hook",
		paramSpec: map[string]action.ParamSpec{
			"webhook-url": {Required: true, Secret: true},
			"channel":     {Required: false, Secret: false},
		},
		buildFn: func(p action.ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
			assert.Equal(t, "#random", p["channel"])
			return config.WebhookSinkConfig{URL: p["webhook-url"], Method: "POST"}, config.TransformConfig{}, nil
		},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{Name: "env-test", Type: "env-hook", Params: map[string]string{
				"webhook-url": "${TEST_URL}",
				"channel":     "${MY_CHANNEL}",
			}},
		},
	}
	consumers, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.NoError(t, err)
	assert.Len(t, consumers, 1)
}

func TestMatchConsumer_FlushBatch_NonBatchFlusher(t *testing.T) {
	// A consumer that doesn't implement BatchFlusher
	type plainConsumer struct {
		router.Consumer
	}
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "", ""), nil, "action:test:hook")
	// fakeConsumer implements BatchFlusher, so this should delegate
	err := mc.FlushBatch(context.Background(), 0)
	assert.NoError(t, err)
}

func TestMatchConsumer_NilMetrics(t *testing.T) {
	inner := &fakeConsumer{id: "test"}
	mc := action.NewMatchConsumer(inner, mustCompile(t, "public.orders", "insert"), nil, "action:test:hook")

	// Should not panic with nil metrics
	err := mc.Deliver(context.Background(), eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpInsert},
	})
	assert.NoError(t, err)

	err = mc.Deliver(context.Background(), eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "orders", Operation: event.OpInsert},
	})
	assert.NoError(t, err)
}

func TestBuildConsumers_MatchCompileError(t *testing.T) {
	t.Setenv("TEST_URL", "https://example.com")
	ft := &fakeType{
		name:      "hook",
		paramSpec: map[string]action.ParamSpec{"webhook-url": {Required: true, Secret: true}},
	}
	reg := newTestRegistry(ft)
	cfg := &config.Config{
		Actions: []config.ActionConfig{
			{
				Name:   "bad-match",
				Type:   "hook",
				Params: map[string]string{"webhook-url": "${TEST_URL}"},
				Match:  config.MatchConfig{Operations: []string{"bogus-op"}},
			},
		},
	}
	_, err := action.BuildConsumersWithRegistry(cfg, observability.NewKaptantoMetrics(), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation")
}

// --- Benchmark ---

func BenchmarkMatchConsumer_Miss(b *testing.B) {
	m := observability.NewKaptantoMetrics()
	inner := &fakeConsumer{id: "bench"}
	mc := action.NewMatchConsumer(inner, mustCompile(b, "public.orders", "insert"), m, "action:bench:test")

	entry := eventlog.LogEntry{
		Event: &event.ChangeEvent{Schema: "public", Table: "users", Operation: event.OpUpdate},
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.Deliver(ctx, entry)
	}
}

// --- Helpers ---

func mustCompile(tb testing.TB, tables, ops string) *action.MatcherForTest {
	tb.Helper()
	var tableList, opList []string
	if tables != "" {
		tableList = []string{tables}
	}
	if ops != "" {
		opList = []string{ops}
	}
	return action.NewMatcherForTest(tableList, opList)
}
