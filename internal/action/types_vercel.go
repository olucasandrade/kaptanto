package action

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// VercelType is the built-in "vercel" action type. It POSTs raw event JSON to a
// Vercel serverless function HTTPS endpoint. An optional bypass-secret is sent
// as x-vercel-protection-bypass for Deployment Protection. ACT-01: data only.
// Batching is allowed.
type VercelType struct{}

func init() { DefaultRegistry.Register(VercelType{}) }

func (VercelType) Name() string { return "vercel" }

func (VercelType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"url": {
			Required:    true,
			Secret:      true,
			Description: "Vercel function HTTPS endpoint URL",
		},
		"bypass-secret": {
			Required:    false,
			Secret:      true,
			Description: "Optional Vercel protection bypass secret (x-vercel-protection-bypass)",
		},
	}
}

func (VercelType) PinsBatch() bool { return false }

func (VercelType) ComputedAuthHeaders() []string {
	return []string{"x-vercel-protection-bypass"}
}

func (VercelType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	url := strings.TrimSpace(p["url"])
	if url == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("vercel: url must not be empty")
	}

	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if secret := p["bypass-secret"]; secret != "" {
		headers["x-vercel-protection-bypass"] = secret
	}

	whCfg := config.WebhookSinkConfig{
		URL:     url,
		Method:  "POST",
		Headers: headers,
	}

	return whCfg, config.TransformConfig{}, nil
}
