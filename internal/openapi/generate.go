package openapi

import (
	"reflect"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/version"
)

// GenerateOptions controls which endpoints and components appear in the spec.
type GenerateOptions struct {
	Output    string       // active output mode (sse, grpc, stdout, etc.)
	AuthToken bool         // whether a bearer token is configured
	Actions   []ActionMeta // action metadata (names + param names only, never values)
}

// NewGenerateOptions builds GenerateOptions from a live config.
func NewGenerateOptions(cfg *config.Config) GenerateOptions {
	opts := GenerateOptions{
		Output:    cfg.Output,
		AuthToken: cfg.AuthToken != "",
	}
	for _, a := range cfg.Actions {
		am := ActionMeta{
			Name:       a.Name,
			Type:       a.Type,
			Tables:     a.Match.Tables,
			Operations: a.Match.Operations,
		}
		for k := range a.Params {
			am.ParamNames = append(am.ParamNames, k)
		}
		opts.Actions = append(opts.Actions, am)
	}
	return opts
}

// Generate builds an OpenAPI 3.0 Document for the running kaptanto instance.
// The result is deterministic: all maps use sorted-key iteration, and
// re-generation with the same options produces identical output (OAS-01).
func Generate(opts GenerateOptions) *Document {
	doc := &Document{
		OpenAPI: "3.0.3",
		Info: Info{
			Title:       "Kaptanto",
			Description: "Universal database Change Data Capture (CDC)",
			Version:     version.Version,
		},
	}

	var security []SecurityReq

	doc.Components = &Components{}

	// ChangeEvent schema from reflection
	ceSchema := ReflectChangeEventSchema(reflect.TypeOf(event.ChangeEvent{}))
	doc.Components.Schemas.Set("ChangeEvent", ceSchema)

	if opts.AuthToken {
		doc.Components.SecuritySchemes.Set("bearerAuth", SecurityScheme{
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "token",
			Description:  "Static bearer token (--auth-token or KAPTANTO_AUTH_TOKEN)",
		})
		security = []SecurityReq{{"bearerAuth": {}}}
	}

	addPaths(doc, opts, security)
	addExtensions(doc, opts)

	return doc
}

func addPaths(doc *Document, opts GenerateOptions, security []SecurityReq) {
	// /healthz is always present for any network output
	if isNetworkOutput(opts.Output) {
		doc.Paths.Set("/healthz", PathItem{
			Get: &Operation{
				Summary:     "Health check",
				OperationID: "healthz",
				Responses:   simpleResponses("200", "Healthy"),
				Security:    security,
			},
		})
	}

	// /metrics is always present for any network output
	if isNetworkOutput(opts.Output) {
		doc.Paths.Set("/metrics", PathItem{
			Get: &Operation{
				Summary:     "Prometheus metrics",
				OperationID: "metrics",
				Responses:   simpleResponses("200", "Prometheus metrics in text format"),
				Security:    security,
			},
		})
	}

	// /events only for SSE output
	if opts.Output == "sse" {
		params := []Parameter{
			{Name: "consumer", In: "query", Description: "Stable consumer ID for cursor tracking"},
			{Name: "tables", In: "query", Description: "Comma-separated table allow-list"},
			{Name: "operations", In: "query", Description: "Comma-separated operation allow-list"},
		}
		var responses orderedMap[Response]
		var content orderedMap[MediaType]
		content.Set("text/event-stream", MediaType{
			Schema: &SchemaRef{Ref: "#/components/schemas/ChangeEvent"},
		})
		responses.Set("200", Response{
			Description: "SSE event stream of ChangeEvents",
			Content:     content,
		})
		doc.Paths.Set("/events", PathItem{
			Get: &Operation{
				Summary:     "Server-Sent Events stream",
				OperationID: "events",
				Parameters:  params,
				Responses:   responses,
				Security:    security,
			},
		})
	}

	// /openapi.json always present for network outputs
	if isNetworkOutput(opts.Output) {
		doc.Paths.Set("/openapi.json", PathItem{
			Get: &Operation{
				Summary:     "OpenAPI specification",
				OperationID: "openapi",
				Responses:   simpleResponses("200", "OpenAPI 3.0 specification"),
				Security:    security,
			},
		})
	}
}

func addExtensions(doc *Document, opts GenerateOptions) {
	if len(opts.Actions) == 0 {
		return
	}
	if doc.Extensions == nil {
		doc.Extensions = make(map[string]any)
	}
	doc.Extensions["x-kaptanto-actions"] = opts.Actions
}

func isNetworkOutput(output string) bool {
	switch output {
	case "sse", "grpc", "nats", "sqs", "kafka", "pubsub", "rabbitmq", "webhook":
		return true
	}
	return false
}

func simpleResponses(code, desc string) orderedMap[Response] {
	var m orderedMap[Response]
	m.Set(code, Response{Description: desc})
	return m
}
