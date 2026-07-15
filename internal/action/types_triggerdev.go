package action

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// triggerdevType implements the "triggerdev" action type (ACT-01).
//
// Trigger.dev accepts events via POST <api-url>/api/v1/events with Bearer auth.
// max-events is pinned to 1 because Trigger.dev's event ingestion API does not
// accept arrays.
type triggerdevType struct{}

func init() { DefaultRegistry.Register(&triggerdevType{}) }

func (*triggerdevType) Name() string { return "triggerdev" }

func (*triggerdevType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"api-key": {
			Required:    true,
			Secret:      true,
			Description: "Trigger.dev API key (sent as Bearer token)",
		},
		"api-url": {
			Required:    false,
			Secret:      false,
			Description: "Trigger.dev API base URL",
			Default:     "https://api.trigger.dev",
		},
		"event-name-template": {
			Required:    false,
			Secret:      false,
			Description: "Go template for the Trigger.dev event name field",
			Default:     "kaptanto/{{.Table}}.{{.Operation}}",
		},
	}
}

func (*triggerdevType) PinsBatch() bool             { return true }
func (*triggerdevType) ComputedAuthHeaders() []string { return []string{"Authorization"} }

func (*triggerdevType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	apiKey := p["api-key"]
	apiURL := strings.TrimRight(p["api-url"], "/")
	nameTemplate := p["event-name-template"]

	if _, err := template.New("").Parse(nameTemplate); err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("triggerdev: invalid event-name-template: %w", err)
	}

	nameExpr := renderNameExpr(nameTemplate)

	whCfg := config.WebhookSinkConfig{
		URL:    apiURL + "/api/v1/events",
		Method: "POST",
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + apiKey,
		},
		Batch: config.WebhookBatch{MaxEvents: 1},
	}

	jqExpr := fmt.Sprintf(
		`{event: {name: %s, payload: ., id: .idempotency_key}}`,
		nameExpr,
	)

	transform := config.TransformConfig{
		Language:   "jq",
		Expression: jqExpr,
	}

	return whCfg, transform, nil
}
