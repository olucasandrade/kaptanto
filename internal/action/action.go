package action

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/output/webhook"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/olucasandrade/kaptanto/internal/routing"
)

// nameRegex validates action names: lowercase alphanumeric and hyphens only.
var nameRegex = regexp.MustCompile(`^[a-z0-9-]+$`)

// envRefRegex validates that a raw param value is an env-var reference.
// Must match after trimming whitespace: ${SOME_VAR}
var envRefRegex = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// BuildConsumers validates all configured actions and returns router.Consumer
// instances ready for registration. Uses DefaultRegistry for type lookup.
func BuildConsumers(cfg *config.Config, m *observability.KaptantoMetrics) ([]router.Consumer, error) {
	return BuildConsumersWithRegistry(cfg, m, DefaultRegistry)
}

// BuildConsumersWithRegistry is the testable variant that accepts an explicit registry.
func BuildConsumersWithRegistry(cfg *config.Config, m *observability.KaptantoMetrics, reg *Registry) ([]router.Consumer, error) {
	if len(cfg.Actions) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(cfg.Actions))
	consumers := make([]router.Consumer, 0, len(cfg.Actions))

	for i := range cfg.Actions {
		a := &cfg.Actions[i]

		// Step 1: validate name
		if err := validateName(a.Name, seen); err != nil {
			return nil, err
		}
		seen[a.Name] = struct{}{}

		// Step 1: validate type exists
		t := reg.Lookup(a.Type)
		if t == nil {
			return nil, fmt.Errorf("action %q: unknown type %q (registered types: %s)",
				a.Name, a.Type, strings.Join(reg.Names(), ", "))
		}

		// Step 1: validate params against ParamSpec
		resolved, err := validateAndResolveParams(a, t)
		if err != nil {
			return nil, err
		}

		// Step 3: compile routing match (RTG-01)
		matcher, err := routing.Compile(routing.MatchConfig{
			Tables:     a.Match.Tables,
			Operations: a.Match.Operations,
		})
		if err != nil {
			return nil, fmt.Errorf("action %q: %w", a.Name, err)
		}

		// Step 4: build webhook config from type
		var whCfg config.WebhookSinkConfig
		var defaultTransform config.TransformConfig

		if a.Type == "custom" {
			// Custom type: verbatim webhook config from ActionConfig.Webhook
			if a.Webhook == nil {
				return nil, fmt.Errorf("action %q: type \"custom\" requires a webhook: block", a.Name)
			}
			whCfg = *a.Webhook
			// For custom, the webhook's own Transform is the default
			defaultTransform = whCfg.Transform
		} else {
			whCfg, defaultTransform, err = t.Build(resolved)
			if err != nil {
				return nil, fmt.Errorf("action %q: type build: %w", a.Name, err)
			}
		}

		// Apply user overrides
		if err := applyOverrides(a, t, &whCfg, defaultTransform); err != nil {
			return nil, err
		}

		// Step 5: construct webhook consumer
		consumerID := fmt.Sprintf("action:%s:%s", a.Type, a.Name)
		inner, err := webhooksink.NewWebhookSinkConsumer(consumerID, whCfg)
		if err != nil {
			return nil, fmt.Errorf("action %q: webhook consumer: %w", a.Name, err)
		}
		inner.SetMetrics(m)

		consumers = append(consumers, &matchConsumer{
			inner:   inner,
			matcher: matcher,
			m:       m,
			id:      consumerID,
		})
	}

	return consumers, nil
}

func validateName(name string, seen map[string]struct{}) error {
	if name == "" {
		return fmt.Errorf("action: name is required")
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("action %q: name must match [a-z0-9-]+ (no uppercase, no ':')", name)
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("action %q: name must not contain ':'", name)
	}
	if _, dup := seen[name]; dup {
		return fmt.Errorf("action %q: duplicate name", name)
	}
	return nil
}

func validateAndResolveParams(a *config.ActionConfig, t Type) (ResolvedParams, error) {
	spec := t.ParamSpec()

	// Check for unknown params
	for k := range a.Params {
		if _, ok := spec[k]; !ok {
			return nil, fmt.Errorf("action %q: unknown param %q for type %q", a.Name, k, a.Type)
		}
	}

	resolved := make(ResolvedParams, len(spec))
	for name, ps := range spec {
		raw, provided := a.Params[name]
		if !provided {
			if ps.Required && ps.Default == "" {
				return nil, fmt.Errorf("action %q: required param %q is missing", a.Name, name)
			}
			if ps.Default != "" {
				resolved[name] = ps.Default
			}
			continue
		}

		// ACT-02: secret enforcement on the RAW value before expansion
		if ps.Secret {
			trimmed := strings.TrimSpace(raw)
			if !envRefRegex.MatchString(trimmed) {
				return nil, fmt.Errorf(
					"action %q: param %q is secret and must be an environment variable reference like ${SLACK_WEBHOOK_URL}",
					a.Name, name)
			}
			// Resolve the env var
			varName := trimmed[2 : len(trimmed)-1] // extract VAR from ${VAR}
			val := os.Getenv(varName)
			if val == "" {
				return nil, fmt.Errorf(
					"action %q: param %q references ${%s} which is unset",
					a.Name, name, varName)
			}
			resolved[name] = val
		} else {
			resolved[name] = expandEnvRefs(raw)
		}
	}

	return resolved, nil
}

// expandEnvRefs expands ${VAR} references in a string value.
func expandEnvRefs(s string) string {
	return os.Expand(s, os.Getenv)
}

func applyOverrides(a *config.ActionConfig, t Type, whCfg *config.WebhookSinkConfig, defaultTransform config.TransformConfig) error {
	// Timeout override
	if a.Timeout != "" {
		whCfg.Timeout = a.Timeout
	}

	// Transform: replaces entirely (no merge)
	if a.Transform != nil {
		whCfg.Transform = *a.Transform
	} else {
		whCfg.Transform = defaultTransform
	}

	// Headers: merge over type defaults; reject computed auth header collisions
	if len(a.Headers) > 0 {
		computed := make(map[string]struct{})
		for _, h := range t.ComputedAuthHeaders() {
			computed[strings.ToLower(h)] = struct{}{}
		}
		if whCfg.Headers == nil {
			whCfg.Headers = make(map[string]string)
		}
		for k, v := range a.Headers {
			if _, collision := computed[strings.ToLower(k)]; collision {
				return fmt.Errorf("action %q: header %q collides with type's computed auth header", a.Name, k)
			}
			whCfg.Headers[k] = v
		}
	}

	// Batch override: rejected if type pins batching
	if a.Batch != nil {
		if t.PinsBatch() {
			return fmt.Errorf("action %q: type %q pins batch.max-events and does not allow override", a.Name, a.Type)
		}
		whCfg.Batch = *a.Batch
	}

	return nil
}

// matchConsumer wraps the webhook consumer with the compiled Matcher (ACT-03).
type matchConsumer struct {
	inner   router.Consumer
	matcher *routing.Matcher
	m       *observability.KaptantoMetrics
	id      string
}

// ID returns the consumer identifier.
func (c *matchConsumer) ID() string { return c.id }

// Deliver evaluates the match before delegating. Non-matching events are acked
// without buffering (ACT-03: cursor advances, like a transform drop).
func (c *matchConsumer) Deliver(ctx context.Context, e eventlog.LogEntry) error {
	if !c.matcher.Match(e.Event) {
		if c.m != nil {
			c.m.ActionEventsSkipped.WithLabelValues(c.id).Inc()
		}
		return nil
	}
	if c.m != nil {
		c.m.ActionEventsMatched.WithLabelValues(c.id).Inc()
	}
	return c.inner.Deliver(ctx, e)
}

// FlushBatch delegates to the inner consumer (required: the router type-asserts
// BatchFlusher; wrapping must not hide it — cursor semantics break otherwise).
func (c *matchConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	if bf, ok := c.inner.(router.BatchFlusher); ok {
		return bf.FlushBatch(ctx, partitionID)
	}
	return nil
}

// Ping delegates to the inner consumer for health checks.
func (c *matchConsumer) Ping() error {
	type pinger interface{ Ping() error }
	if p, ok := c.inner.(pinger); ok {
		return p.Ping()
	}
	return nil
}

// Close delegates to the inner consumer for graceful shutdown.
func (c *matchConsumer) Close() {
	type closer interface{ Close() }
	if cl, ok := c.inner.(closer); ok {
		cl.Close()
	}
}

// SetMetrics delegates to the inner consumer.
func (c *matchConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	type metricsAware interface{ SetMetrics(*observability.KaptantoMetrics) }
	if ma, ok := c.inner.(metricsAware); ok {
		ma.SetMetrics(m)
	}
}
