package action

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/config"
)

// pineconeType implements Type for the "vector-upsert" action (Pinecone vector
// database). It constructs a POST to the Pinecone vectors/upsert endpoint with
// Api-Key auth and a jq transform that builds the upsert payload from event
// data. Delete operations emit null (drop — cursor advances, no request).
type pineconeType struct{}

func init() { DefaultRegistry.Register(&pineconeType{}) }

func (pineconeType) Name() string { return "vector-upsert" }

func (pineconeType) ParamSpec() map[string]ParamSpec {
	return map[string]ParamSpec{
		"api-key":      {Required: true, Secret: true, Description: "Pinecone API key"},
		"index-host":   {Required: true, Secret: false, Description: "Pinecone index host (e.g. my-index-abc123.svc.us-east1-gcp.pinecone.io)"},
		"namespace":    {Required: false, Secret: false, Description: "Pinecone namespace"},
		"id-field":     {Required: false, Secret: false, Description: "Event field to use as vector ID", Default: "id"},
		"vector-field": {Required: true, Secret: false, Description: "Event field containing the embedding vector"},
	}
}

func (pineconeType) PinsBatch() bool             { return false }
func (pineconeType) ComputedAuthHeaders() []string { return []string{"Api-Key"} }

func (pineconeType) Build(p ResolvedParams) (config.WebhookSinkConfig, config.TransformConfig, error) {
	host := p["index-host"]
	if host == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("index-host must not be empty")
	}

	vectorField := p["vector-field"]
	if vectorField == "" {
		return config.WebhookSinkConfig{}, config.TransformConfig{},
			fmt.Errorf("vector-field must not be empty")
	}

	idField := p["id-field"]
	if idField == "" {
		idField = "id"
	}
	namespace := p["namespace"]

	host = strings.TrimPrefix(host, "https://")

	whCfg := config.WebhookSinkConfig{
		URL:    fmt.Sprintf("https://%s/vectors/upsert", host),
		Method: "POST",
		Headers: map[string]string{
			"Api-Key":      p["api-key"],
			"Content-Type": "application/json",
		},
	}

	expr := buildPineconeJQ(idField, vectorField, namespace)

	tc := config.TransformConfig{
		Language:   "jq",
		Expression: expr,
	}

	return whCfg, tc, nil
}

// buildPineconeJQ constructs the jq expression that transforms a ChangeEvent
// into a Pinecone upsert payload, dropping delete operations.
func buildPineconeJQ(idField, vectorField, namespace string) string {
	idAccess := fieldAccess("after", idField)
	vectorAccess := fieldAccess("after", vectorField)

	vectorObj := fmt.Sprintf(
		`{vectors: [{id: (%s | tostring), values: %s}]}`,
		idAccess, vectorAccess,
	)

	if namespace != "" {
		vectorObj = fmt.Sprintf(
			`{vectors: [{id: (%s | tostring), values: %s}], namespace: %q}`,
			idAccess, vectorAccess, namespace,
		)
	}

	return fmt.Sprintf(
		`if .operation == "delete" then null else %s end`,
		vectorObj,
	)
}

// fieldAccess returns a safe jq field access like `.after["embedding-vector"]`.
func fieldAccess(side, field string) string {
	return fmt.Sprintf(".%s[%s]", side, jqStringLiteral(field))
}
