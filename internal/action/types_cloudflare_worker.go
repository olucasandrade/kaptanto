package action

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// CloudflareWorkerType is the built-in "cloudflare-worker" action type. It
// POSTs raw event JSON to a Worker HTTPS endpoint with an optional static auth
// header. ACT-01: data only. Batching is allowed.
type CloudflareWorkerType struct{}

func init() { DefaultRegistry.Register(CloudflareWorkerType{}) }

func (CloudflareWorkerType) Name() string { return "cloudflare-worker" }

func (CloudflareWorkerType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"url": {
			Required:    true,
			Secret:      true,
			Description: "Cloudflare Worker HTTPS endpoint URL",
		},
		"auth-header-name": {
			Required:    false,
			Secret:      false,
			Description: "Header name for the optional auth token",
			Default:     "Authorization",
		},
		"auth-token": {
			Required:    false,
			Secret:      true,
			Description: "Optional static auth token value for auth-header-name",
		},
		"allow-unauthenticated": {
			Required:    false,
			Secret:      false,
			Description: "Set to true to POST CDC JSON without auth-token (unsafe for sensitive tables)",
		},
	}
}

func (CloudflareWorkerType) PinsBatch() bool { return false }

func (CloudflareWorkerType) ComputedAuthHeaders() []string {
	// Protect the default auth header name. Custom auth-header-name values are
	// still set by Build; callers should avoid colliding via headers: overrides.
	return []string{"Authorization"}
}

func (CloudflareWorkerType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	url := strings.TrimSpace(p["url"])
	if url == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("cloudflare-worker: url must not be empty")
	}

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	token := strings.TrimSpace(p["auth-token"])
	if token == "" && !cloudflareWorkerAllowsUnauthenticated(p["allow-unauthenticated"]) {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("cloudflare-worker: auth-token is empty; set auth-token or allow-unauthenticated: true to POST CDC JSON without auth")
	}
	if token != "" {
		hdrName := strings.TrimSpace(p["auth-header-name"])
		if hdrName == "" {
			hdrName = "Authorization"
		}
		headers[hdrName] = token
	}

	whCfg := config.WebhookSinkConfig{
		URL:     url,
		Method:  "POST",
		Headers: headers,
	}

	return whCfg, config.TransformConfig{}, nil
}

func cloudflareWorkerAllowsUnauthenticated(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
