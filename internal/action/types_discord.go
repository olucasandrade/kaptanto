package action

import (
	"github.com/olucasandrade/kaptanto/internal/config"
)

// discordDefaultTransform is a jq expression that produces a Discord webhook
// payload with a plain-text summary of the CDC event.
const discordDefaultTransform = `{content: ("[" + .operation + "] " + .table + " — key " + (.key // "?" | tostring))}`

// DiscordType is the built-in "discord" action type. It is a pure preset over
// the webhook sink (ACT-01): no delivery code, only data.
//
// Targets Discord Webhook URLs — a single POST per event with a JSON body
// containing the "content" field. Batching is pinned to 1.
// HMAC signing is not available (Discord controls the webhook URL).
type DiscordType struct{}

func (DiscordType) Name() string { return "discord" }

func (DiscordType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"webhook-url": {Required: true, Secret: true, Description: "Discord webhook URL"},
	}
}

func (DiscordType) PinsBatch() bool            { return true }
func (DiscordType) ComputedAuthHeaders() []string { return nil }

func (DiscordType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
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
		Expression: discordDefaultTransform,
	}
	return whCfg, tfCfg, nil
}

func init() { DefaultRegistry.Register(DiscordType{}) }
