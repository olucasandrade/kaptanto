package action

import (
	"fmt"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// cloudflareType implements Type for the "cache-invalidate" action (Cloudflare
// cache purge). It constructs a POST to the Cloudflare purge_cache API with
// Bearer auth, a go-template body rendering the purge URL into the files array,
// and pinned batching (exactly one event per request).
type cloudflareType struct{}

func init() { DefaultRegistry.Register(&cloudflareType{}) }

func (cloudflareType) Name() string { return "cache-invalidate" }

func (cloudflareType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"api-token":    {Required: true, Secret: true, Description: "Cloudflare API token"},
		"zone-id":      {Required: true, Secret: false, Description: "Cloudflare zone ID"},
		"url-template": {Required: true, Secret: false, Description: "Go template rendering the purge URL from the event"},
	}
}

func (cloudflareType) PinsBatch() bool          { return true }
func (cloudflareType) ComputedAuthHeaders() []string { return []string{"Authorization"} }

func (cloudflareType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	zoneID := p["zone-id"]
	if zoneID == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("zone-id must not be empty")
	}

	urlTmpl := p["url-template"]
	if urlTmpl == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("url-template must not be empty")
	}

	whCfg := config.WebhookSinkConfig{
		URL:    fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/purge_cache", zoneID),
		Method: "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Auth: config.WebhookAuthConfig{
			BearerToken: p["api-token"],
		},
		Batch: config.WebhookBatch{MaxEvents: 1},
	}

	transformExpr := `{"files":["` + urlTmpl + `"]}`

	tc := config.TransformConfig{
		Language:   "go-template",
		Expression: transformExpr,
	}

	return whCfg, tc, nil
}
