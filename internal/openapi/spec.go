// Package openapi provides deterministic OpenAPI 3.0.3 spec generation and an
// HTTP handler that serves the cached spec with ETag-based caching.
//
// The package uses hand-rolled struct types (no third-party OpenAPI library) and
// marshals with sorted keys for golden-file testability (OAS-01).
package openapi

// Document is a minimal OpenAPI 3.0.3 root object.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Paths      orderedMap[PathItem]  `json:"paths"`
	Components *Components           `json:"components,omitempty"`
	Extensions map[string]any        `json:"-"`
}

// Info holds API metadata.
type Info struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

// PathItem describes the operations available on a single path.
type PathItem struct {
	Get  *Operation `json:"get,omitempty"`
	Post *Operation `json:"post,omitempty"`
}

// Operation describes a single API operation.
type Operation struct {
	Summary     string               `json:"summary,omitempty"`
	OperationID string               `json:"operationId,omitempty"`
	Parameters  []Parameter          `json:"parameters,omitempty"`
	Responses   orderedMap[Response] `json:"responses"`
	Security    []SecurityReq        `json:"security,omitempty"`
}

// Parameter describes a single operation parameter.
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

// Response describes a single response from an API operation.
type Response struct {
	Description string                  `json:"description"`
	Content     orderedMap[MediaType]   `json:"content,omitempty"`
}

// MediaType describes a media type with optional schema.
type MediaType struct {
	Schema *SchemaRef `json:"schema,omitempty"`
}

// SchemaRef is either an inline Schema or a $ref string.
type SchemaRef struct {
	Ref    string  `json:"$ref,omitempty"`
	Schema *Schema `json:"-"`
}

// Schema is a simplified JSON Schema subset for OpenAPI 3.0.
// Uses a custom MarshalJSON to omit empty orderedMap fields.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           orderedMap[Schema]  `json:"-"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
}

// Components holds reusable spec fragments.
type Components struct {
	Schemas         orderedMap[Schema]         `json:"schemas,omitempty"`
	SecuritySchemes orderedMap[SecurityScheme]  `json:"securitySchemes,omitempty"`
}

// SecurityScheme describes an authentication mechanism.
type SecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	Description  string `json:"description,omitempty"`
}

// SecurityReq is a security requirement object (map of scheme name to scopes).
type SecurityReq map[string][]string

// ActionMeta holds safe-to-expose metadata about a configured action.
// Secret param values are NEVER included (ACT-02).
type ActionMeta struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Tables     []string `json:"tables,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Where      string   `json:"where,omitempty"`
	ParamNames []string `json:"param_names"`
}
