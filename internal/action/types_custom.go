package action

import (
	"fmt"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// customType is the "custom" escape hatch: the action's webhook sub-block is
// a full WebhookSinkConfig taken verbatim, validated by the Group 1 webhook
// constructor. Keeps Group 1's permissive expansion (expand-to-empty).
type customType struct{}

func init() {
	DefaultRegistry.Register(&customType{})
}

func (c *customType) Name() string { return "custom" }

func (c *customType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{}
}

func (c *customType) Build(_ ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	return config.WebhookSinkConfig{}, config.TransformConfig{}, fmt.Errorf("custom type must be built via BuildCustom")
}

func (c *customType) PinsBatch() bool          { return false }
func (c *customType) ComputedAuthHeaders() []string { return nil }
