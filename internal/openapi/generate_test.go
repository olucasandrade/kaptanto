package openapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
)

func TestReflectChangeEventSchema(t *testing.T) {
	s := ReflectChangeEventSchema(reflect.TypeOf(event.ChangeEvent{}))

	if s.Type != "object" {
		t.Fatalf("expected type=object, got %q", s.Type)
	}

	entries := s.Properties.Entries()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Key
	}

	want := []string{
		"id", "idempotency_key", "timestamp", "source", "operation",
		"database", "schema", "table", "key", "before", "after", "metadata",
		"ai_context",
	}
	sort.Strings(want)
	sort.Strings(names)

	if !reflect.DeepEqual(names, want) {
		t.Fatalf("properties mismatch:\n got:  %v\n want: %v", names, want)
	}

	// Check required fields do NOT include omitempty fields
	for _, r := range s.Required {
		if r == "database" || r == "schema" || r == "ai_context" {
			t.Errorf("%q should NOT be required (has omitempty)", r)
		}
	}

	// Verify omitempty fields are not in required
	reqSet := make(map[string]bool)
	for _, r := range s.Required {
		reqSet[r] = true
	}
	if reqSet["database"] || reqSet["schema"] || reqSet["ai_context"] {
		t.Error("omitempty fields appeared in required list")
	}

	// Verify id field has ulid format
	for _, e := range entries {
		if e.Key == "id" {
			if e.Value.Format != "ulid" {
				t.Errorf("id field format = %q, want ulid", e.Value.Format)
			}
		}
		if e.Key == "timestamp" {
			if e.Value.Format != "date-time" {
				t.Errorf("timestamp field format = %q, want date-time", e.Value.Format)
			}
		}
		if e.Key == "operation" {
			if e.Value.Type != "string" {
				t.Errorf("operation field type = %q, want string", e.Value.Type)
			}
		}
	}
}

func TestGenerateDeterministic(t *testing.T) {
	opts := GenerateOptions{
		Output:    "sse",
		AuthToken: true,
		Actions: []ActionMeta{
			{Name: "notify", Type: "slack", ParamNames: []string{"channel"}},
		},
	}

	doc1 := Generate(opts)
	b1, err := MarshalDocument(doc1)
	if err != nil {
		t.Fatal(err)
	}

	doc2 := Generate(opts)
	b2, err := MarshalDocument(doc2)
	if err != nil {
		t.Fatal(err)
	}

	if string(b1) != string(b2) {
		t.Fatal("OAS-01 violated: two generations produced different bytes")
	}

	if !json.Valid(b1) {
		t.Fatal("generated spec is not valid JSON")
	}
}

func TestGenerateEndpointPresence_SSE(t *testing.T) {
	opts := GenerateOptions{Output: "sse", AuthToken: false}
	doc := Generate(opts)

	paths := doc.Paths.Entries()
	pathNames := make(map[string]bool)
	for _, e := range paths {
		pathNames[e.Key] = true
	}

	for _, want := range []string{"/events", "/healthz", "/metrics", "/openapi.json"} {
		if !pathNames[want] {
			t.Errorf("missing path %q for SSE output", want)
		}
	}
}

func TestGenerateEndpointPresence_Stdout(t *testing.T) {
	opts := GenerateOptions{Output: "stdout", AuthToken: false}
	doc := Generate(opts)

	if doc.Paths.Len() != 0 {
		t.Errorf("stdout output should have no paths, got %d", doc.Paths.Len())
	}
}

func TestGenerateEndpointPresence_SinkOutputs(t *testing.T) {
	for _, output := range []string{"kafka", "nats", "sqs", "pubsub", "rabbitmq"} {
		t.Run(output, func(t *testing.T) {
			opts := GenerateOptions{Output: output, AuthToken: false}
			doc := Generate(opts)

			paths := doc.Paths.Entries()
			pathNames := make(map[string]bool)
			for _, e := range paths {
				pathNames[e.Key] = true
			}

			if !pathNames["/healthz"] {
				t.Error("missing /healthz")
			}
			if !pathNames["/metrics"] {
				t.Error("missing /metrics")
			}
			if pathNames["/events"] {
				t.Errorf("/events should not be present for %s output", output)
			}
		})
	}
}

func TestGenerateSecurityScheme(t *testing.T) {
	t.Run("with auth", func(t *testing.T) {
		doc := Generate(GenerateOptions{Output: "sse", AuthToken: true})
		if doc.Components == nil || doc.Components.SecuritySchemes.Len() == 0 {
			t.Fatal("expected securitySchemes with auth")
		}
	})

	t.Run("without auth", func(t *testing.T) {
		doc := Generate(GenerateOptions{Output: "sse", AuthToken: false})
		if doc.Components != nil && doc.Components.SecuritySchemes.Len() > 0 {
			t.Error("expected no securitySchemes without auth")
		}
	})
}

func TestGenerateActionsExtension(t *testing.T) {
	opts := GenerateOptions{
		Output: "sse",
		Actions: []ActionMeta{
			{Name: "alert", Type: "pagerduty", ParamNames: []string{"routing_key"}},
		},
	}
	doc := Generate(opts)

	raw, ok := doc.Extensions["x-kaptanto-actions"]
	if !ok {
		t.Fatal("missing x-kaptanto-actions extension")
	}

	actions, ok := raw.([]ActionMeta)
	if !ok || len(actions) != 1 {
		t.Fatal("x-kaptanto-actions wrong type or length")
	}
	if actions[0].Name != "alert" || actions[0].Type != "pagerduty" {
		t.Errorf("unexpected action: %+v", actions[0])
	}
}

func TestSecretScan(t *testing.T) {
	secrets := []string{
		"super-secret-token",
		"my-webhook-url",
		"password123",
		"sk_live_test",
	}

	opts := GenerateOptions{
		Output:    "sse",
		AuthToken: true,
		Actions: []ActionMeta{
			{
				Name:       "notify",
				Type:       "slack",
				ParamNames: []string{"channel"},
			},
		},
	}
	doc := Generate(opts)
	b, err := MarshalDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	specStr := string(b)

	for _, secret := range secrets {
		if strings.Contains(specStr, secret) {
			t.Errorf("spec contains secret substring %q", secret)
		}
	}

	// Verify param names appear but not values
	if !strings.Contains(specStr, "channel") {
		t.Error("expected param name 'channel' in spec")
	}
}
