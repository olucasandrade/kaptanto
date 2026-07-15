package action

import (
	"github.com/olucasandrade/kaptanto/internal/config"
)

// httpRequestType is the "http-request" action type: a thin preset that POSTs
// raw event JSON to a URL with standard webhook headers. No default transform.
type httpRequestType struct{}

func init() {
	DefaultRegistry.Register(&httpRequestType{})
}

func (h *httpRequestType) Name() string { return "http-request" }

func (h *httpRequestType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"url": {
			Required:    true,
			Secret:      true,
			Description: "Destination URL (may embed tokens; treated as secret)",
		},
		"method": {
			Required:    false,
			Secret:      false,
			Description: "HTTP method (default POST)",
			Default:     "POST",
		},
	}
}

func (h *httpRequestType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	return config.WebhookSinkConfig{
		URL:    p["url"],
		Method: p["method"],
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}, config.TransformConfig{}, nil
}

func (h *httpRequestType) PinsBatch() bool          { return false }
func (h *httpRequestType) ComputedAuthHeaders() []string { return nil }
