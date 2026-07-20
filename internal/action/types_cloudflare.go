package action

import (
	"fmt"
	"sort"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// cloudflareType implements Type for the "cache-invalidate" action (Cloudflare
// cache purge). It constructs a POST to the Cloudflare purge_cache API with
// Bearer auth, a jq transform body that safely JSON-encodes the rendered purge
// URL into the files array, and pinned batching (exactly one event per request).
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

func (cloudflareType) PinsBatch() bool             { return true }
func (cloudflareType) ComputedAuthHeaders() []string { return []string{"Authorization"} }

// urlFieldMap maps Go-template field names to jq JSON paths on the ChangeEvent.
var urlFieldMap = map[string]string{
	"Operation":      ".operation",
	"Table":          ".table",
	"Schema":         ".schema",
	"Database":       ".database",
	"Source":         ".source",
	"ID":             ".id",
	"IdempotencyKey": ".idempotency_key",
}

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

	urlExpr, err := urlTemplateToJQ(urlTmpl)
	if err != nil {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("url-template: %w", err)
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

	transformExpr := fmt.Sprintf(`{"files":[%s]}`, urlExpr)

	tc := config.TransformConfig{
		Language:   "jq",
		Expression: transformExpr,
	}

	return whCfg, tc, nil
}

// urlTemplateToJQ converts a simple Go-template URL string like
// "https://cdn.example.com/{{.Table}}" into a jq string expression that safely
// JSON-encodes the rendered URL. Unsupported Go-template constructs are
// rejected so they cannot silently produce malformed JSON.
func urlTemplateToJQ(tmpl string) (string, error) {
	matches := templateFieldRe.FindAllStringSubmatchIndex(tmpl, -1)
	if len(matches) == 0 {
		if strings.Contains(tmpl, "{{") || strings.Contains(tmpl, "}}") {
			return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
		}
		return jqStringLiteral(tmpl), nil
	}

	var parts []string
	lastEnd := 0

	for _, loc := range matches {
		if loc[0] > lastEnd {
			literal := tmpl[lastEnd:loc[0]]
			if strings.Contains(literal, "{{") || strings.Contains(literal, "}}") {
				return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
			}
			parts = append(parts, jqStringLiteral(literal))
		}

		fieldName := tmpl[loc[2]:loc[3]]
		jqField, ok := urlFieldMap[fieldName]
		if !ok {
			return "", fmt.Errorf("unsupported field %q; supported: %s",
				fieldName, strings.Join(sortedKeys(urlFieldMap), ", "))
		}
		parts = append(parts, fmt.Sprintf("(%s | tostring)", jqField))
		lastEnd = loc[1]
	}

	if lastEnd < len(tmpl) {
		literal := tmpl[lastEnd:]
		if strings.Contains(literal, "{{") || strings.Contains(literal, "}}") {
			return "", fmt.Errorf("unsupported template syntax in %q; only {{.Field}} placeholders are allowed", tmpl)
		}
		parts = append(parts, jqStringLiteral(literal))
	}

	return "(" + strings.Join(parts, " + ") + ")", nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
