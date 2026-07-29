package action

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// LambdaType is the built-in "lambda" action type. It POSTs raw event JSON to
// an AWS Lambda Function URL with SigV4 auth (service "lambda"). ACT-01: data
// only — delivery goes through the webhook sink.
//
// invocation=async sets X-Amz-Invocation-Type: Event; Lambda returns 202, which
// the webhook sink already treats as success (2xx). Batching is pinned to 1.
type LambdaType struct{}

func init() { DefaultRegistry.Register(LambdaType{}) }

func (LambdaType) Name() string { return "lambda" }

func (LambdaType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"function-url": {
			Required:    true,
			Secret:      true,
			Description: "AWS Lambda Function URL",
		},
		"region": {
			Required:    true,
			Secret:      false,
			Description: "AWS region for SigV4 signing (e.g. us-east-1)",
		},
		"invocation": {
			Required:    false,
			Secret:      false,
			Description: "Invocation mode: sync (default) or async",
			Default:     "sync",
		},
	}
}

func (LambdaType) PinsBatch() bool { return true }

func (LambdaType) ComputedAuthHeaders() []string {
	return []string{"Authorization", "X-Amz-Invocation-Type"}
}

func (LambdaType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	functionURL := strings.TrimSpace(p["function-url"])
	if functionURL == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("lambda: function-url must not be empty")
	}

	region := strings.TrimSpace(p["region"])
	if region == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("lambda: region must not be empty")
	}

	invocation := strings.TrimSpace(p["invocation"])
	if invocation == "" {
		invocation = "sync"
	}
	switch invocation {
	case "sync", "async":
	default:
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("lambda: invocation must be \"sync\" or \"async\", got %q", invocation)
	}

	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if invocation == "async" {
		headers["X-Amz-Invocation-Type"] = "Event"
	}

	whCfg := config.WebhookSinkConfig{
		URL:     functionURL,
		Method:  "POST",
		Headers: headers,
		Auth: config.WebhookAuthConfig{
			AWSSigV4: &config.WebhookSigV4{
				Region:  region,
				Service: "lambda",
			},
		},
		Batch: config.WebhookBatch{MaxEvents: 1},
	}

	return whCfg, config.TransformConfig{}, nil
}
