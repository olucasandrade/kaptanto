package openapi

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/config"
)

func TestNewGenerateOptions(t *testing.T) {
	cfg := &config.Config{
		Output:    "sse",
		AuthToken: "my-secret",
		Actions: []config.ActionConfig{
			{
				Name: "notify",
				Type: "slack",
				Params: map[string]string{
					"webhook_url": "${SLACK_URL}",
					"channel":     "#alerts",
				},
				Match: config.MatchConfig{
					Tables:     []string{"public.orders"},
					Operations: []string{"insert"},
				},
			},
		},
	}

	opts := NewGenerateOptions(cfg)

	if opts.Output != "sse" {
		t.Errorf("output = %q, want sse", opts.Output)
	}
	if !opts.AuthToken {
		t.Error("expected AuthToken=true")
	}
	if len(opts.Actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(opts.Actions))
	}
	a := opts.Actions[0]
	if a.Name != "notify" {
		t.Errorf("action name = %q", a.Name)
	}
	if a.Type != "slack" {
		t.Errorf("action type = %q", a.Type)
	}
	if len(a.ParamNames) != 2 {
		t.Errorf("param names len = %d, want 2", len(a.ParamNames))
	}
	if len(a.Tables) != 1 || a.Tables[0] != "public.orders" {
		t.Errorf("tables = %v", a.Tables)
	}
	if len(a.Operations) != 1 || a.Operations[0] != "insert" {
		t.Errorf("operations = %v", a.Operations)
	}
}

func TestNewGenerateOptions_NoAuth(t *testing.T) {
	cfg := &config.Config{
		Output:    "kafka",
		AuthToken: "",
	}
	opts := NewGenerateOptions(cfg)
	if opts.AuthToken {
		t.Error("expected AuthToken=false when empty")
	}
	if len(opts.Actions) != 0 {
		t.Error("expected no actions")
	}
}
