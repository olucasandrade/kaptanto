package action

import (
	"github.com/olucasandrade/kaptanto/internal/config"
)

// slackDefaultTransform is a jq expression that produces a Slack incoming
// webhook payload with a plain-text summary of the CDC event.
const slackDefaultTransform = `{text: ("[" + .operation + "] " + .table + " — key " + (.key // "?" | tostring))}`

// SlackType is the built-in "slack" action type. It is a pure preset over the
// webhook sink (ACT-01): no delivery code, only data.
//
// Targets Slack Incoming Webhooks — a single POST per event with a JSON body
// containing the "text" field. Batching is pinned to 1.
// HMAC signing is not available (Slack controls the webhook URL).
type SlackType struct{}

func (SlackType) Name() string { return "slack" }

func (SlackType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"webhook-url": {Required: true, Secret: true, Description: "Slack incoming webhook URL"},
	}
}

func (SlackType) PinsBatch() bool            { return true }
func (SlackType) ComputedAuthHeaders() []string { return nil }

func (SlackType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	whCfg := config.WebhookSinkConfig{
		URL:    p["webhook-url"],
		Method: "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Batch: config.WebhookBatch{MaxEvents: 1},
	}
	tfCfg := config.TransformConfig{
		Language:   "jq",
		Expression: slackDefaultTransform,
	}
	return whCfg, tfCfg, nil
}

func init() { DefaultRegistry.Register(SlackType{}) }
