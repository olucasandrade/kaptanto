// Package action implements the action registry core (ACT-01, ACT-02, ACT-03).
//
// An action type is ONLY data — a function from (params, secrets) to a webhook
// sink config + transform expression. Types must not add delivery code paths;
// every byte on the wire goes through the Group 1 webhook sink.
package action

import (
	"github.com/olucasandrade/kaptanto/internal/config"
)

// Type defines a built-in action type (ACT-01: data only).
type Type interface {
	// Name returns the action type identifier (e.g. "slack", "pagerduty").
	Name() string

	// ParamSpec declares accepted params: name -> {required, secret, description, default}.
	// Used for validation, redaction (ACT-02), and OpenAPI metadata.
	ParamSpec() map[string]ParamSpec

	// Build assembles the webhook sink config + default transform from validated,
	// env-resolved params. Must not perform I/O.
	Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error)

	// PinsBatch returns true if this type pins batch.max-events and rejects user
	// overrides. Types that require exactly one event per request (e.g. Slack
	// incoming webhooks) should return true.
	PinsBatch() bool

	// ComputedAuthHeaders returns the set of header names that the type's Build
	// method computes as authentication headers. User-supplied headers that
	// collide with these names are rejected at startup.
	ComputedAuthHeaders() []string
}

// ParamSpec describes a single parameter accepted by a Type.
type ParamSpec struct {
	Required    bool   // must be provided in config
	Secret      bool   // must be an env-var reference (ACT-02)
	Description string // human-readable description
	Default     string // default value; "" means no default
}

// ResolvedParams holds the final param values after env-var expansion.
// Keys are param names; values are the resolved strings.
type ResolvedParams map[string]string
