package openapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// ReflectChangeEventSchema builds a Schema from the json tags of the
// event.ChangeEvent struct type. It inspects the struct via reflection so the
// OpenAPI spec stays in sync with the Go type automatically.
func ReflectChangeEventSchema(t reflect.Type) Schema {
	s := Schema{Type: "object"}

	var required []string
	var props orderedMap[Schema]

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}

		name, opts := parseJSONTag(tag)
		if name == "" {
			name = f.Name
		}

		fieldSchema := typeToSchema(f.Type)
		if f.Tag.Get("json") != "" {
			if desc := fieldComment(name); desc != "" {
				fieldSchema.Description = desc
			}
		}

		props.Set(name, fieldSchema)
		if !opts.omitempty {
			required = append(required, name)
		}
	}

	s.Properties = props
	if len(required) > 0 {
		s.Required = required
	}
	return s
}

func typeToSchema(t reflect.Type) Schema {
	switch t {
	case reflect.TypeOf(ulid.ULID{}):
		return Schema{Type: "string", Format: "ulid"}
	case reflect.TypeOf(time.Time{}):
		return Schema{Type: "string", Format: "date-time"}
	case reflect.TypeOf(json.RawMessage{}):
		return Schema{Description: "arbitrary JSON value"}
	}

	switch t.Kind() {
	case reflect.String:
		return Schema{Type: "string"}
	case reflect.Bool:
		return Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Schema{Type: "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return Schema{Type: "number"}
	case reflect.Slice:
		items := typeToSchema(t.Elem())
		return Schema{Type: "array", Items: &items}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			valSchema := typeToSchema(t.Elem())
			return Schema{Type: "object", AdditionalProperties: &valSchema}
		}
		return Schema{Type: "object"}
	case reflect.Pointer:
		return typeToSchema(t.Elem())
	case reflect.Interface:
		return Schema{}
	case reflect.Struct:
		return reflectStruct(t)
	default:
		return Schema{}
	}
}

func reflectStruct(t reflect.Type) Schema {
	s := Schema{Type: "object"}
	var props orderedMap[Schema]
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _ := parseJSONTag(tag)
		if name == "" {
			name = f.Name
		}
		props.Set(name, typeToSchema(f.Type))
	}
	s.Properties = props
	return s
}

type tagOpts struct {
	omitempty bool
}

func parseJSONTag(tag string) (string, tagOpts) {
	if tag == "" {
		return "", tagOpts{}
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	opts := tagOpts{}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			opts.omitempty = true
		}
	}
	return name, opts
}

// fieldComment returns a human-readable description for well-known ChangeEvent
// fields. This avoids parsing Go source comments at runtime.
func fieldComment(name string) string {
	switch name {
	case "id":
		return "Time-ordered ULID identifier"
	case "idempotency_key":
		return "Unique key for deduplication"
	case "timestamp":
		return "Wall-clock time when the change was captured"
	case "source":
		return "Database connection identifier"
	case "operation":
		return "Type of change: insert, update, delete, read, control"
	case "database":
		return "Database name"
	case "schema":
		return "Schema/namespace"
	case "table":
		return "Table or collection name"
	case "key":
		return "Primary key column(s) as a JSON object"
	case "before":
		return "Row state before the change; null for inserts and reads"
	case "after":
		return "Row state after the change; null for deletes"
	case "metadata":
		return "Source-specific fields (e.g., LSN, checkpoint)"
	case "ai_context":
		return "Optional opaque AI-generated metadata attached by enrichment. Documented shape: {intent?: string, entities?: [{type, value, field?}], suggested_actions?: [string], embedding?: {model: string, vector: [float...]}, custom?: object}"
	default:
		return ""
	}
}
