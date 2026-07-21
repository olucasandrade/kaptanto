package action

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// inngestType implements the "inngest" action type (ACT-01).
//
// Inngest accepts events via POST <api-url>/e/<event-key> (cloud default:
// https://inn.gs). The event-key is embedded in the URL path (not a header), so
// there are no computed auth headers. Inngest accepts JSON arrays, so batching
// is allowed.
type inngestType struct{}

func init() { DefaultRegistry.Register(&inngestType{}) }

func (*inngestType) Name() string { return "inngest" }

func (*inngestType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"event-key": {
			Required:    true,
			Secret:      true,
			Description: "Inngest event key (embedded in URL path)",
		},
		"api-url": {
			Required:    false,
			Secret:      false,
			Description: "Inngest API base URL (set to a local dev server for development)",
			Default:     "https://inn.gs",
		},
		"event-name-template": {
			Required:    false,
			Secret:      false,
			Description: "Go template for the Inngest event name field",
			Default:     "kaptanto/{{.Table}}.{{.Operation}}",
		},
	}
}

func (*inngestType) PinsBatch() bool               { return false }
func (*inngestType) ComputedAuthHeaders() []string { return nil }

func (*inngestType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	eventKey := p["event-key"]
	apiURL := strings.TrimRight(p["api-url"], "/")
	if apiURL == "" {
		apiURL = "https://inn.gs"
	}
	nameTemplate := p["event-name-template"]

	if err := validateEventNameTemplate(nameTemplate); err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("inngest: invalid event-name-template: %w", err)
	}

	nameExpr, err := renderEventNameExpr(nameTemplate)
	if err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("inngest: invalid event-name-template: %w", err)
	}

	url := apiURL + "/e/" + eventKey

	whCfg := config.WebhookSinkConfig{
		URL:    url,
		Method: "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}

	jqExpr := fmt.Sprintf(
		`{name: %s, id: .idempotency_key, ts: ((.timestamp | fromdateiso8601) * 1000) // (now*1000 | floor), data: .}`,
		nameExpr,
	)

	transform := config.TransformConfig{
		Language:   "jq",
		Expression: jqExpr,
	}

	return whCfg, transform, nil
}
