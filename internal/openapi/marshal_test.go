package openapi

import (
	"encoding/json"
	"testing"
)

func TestMarshalDocument_Valid(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.0.3",
		Info:    Info{Title: "Test", Version: "1.0"},
	}

	b, err := MarshalDocument(doc)
	if err != nil {
		t.Fatal(err)
	}

	if !json.Valid(b) {
		t.Fatalf("output is not valid JSON: %s", string(b))
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["openapi"] != "3.0.3" {
		t.Errorf("openapi = %v", parsed["openapi"])
	}
}

func TestMarshalDocument_WithExtensions(t *testing.T) {
	doc := &Document{
		OpenAPI:    "3.0.3",
		Info:       Info{Title: "Test", Version: "1.0"},
		Extensions: map[string]any{"x-custom": "value"},
	}

	b, err := MarshalDocument(doc)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["x-custom"] != "value" {
		t.Errorf("x-custom = %v", parsed["x-custom"])
	}
}

func TestSchemaMarshalJSON_AllFields(t *testing.T) {
	items := Schema{Type: "string"}
	addlProps := Schema{Type: "integer"}
	var props orderedMap[Schema]
	props.Set("field1", Schema{Type: "string"})

	s := Schema{
		Type:                 "object",
		Format:               "custom",
		Description:          "test schema",
		Properties:           props,
		Items:                &items,
		Enum:                 []string{"a", "b"},
		Required:             []string{"field1"},
		AdditionalProperties: &addlProps,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("invalid JSON: %s", string(b))
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "object" {
		t.Errorf("type = %v", parsed["type"])
	}
	if parsed["format"] != "custom" {
		t.Errorf("format = %v", parsed["format"])
	}
	if parsed["description"] != "test schema" {
		t.Errorf("description = %v", parsed["description"])
	}
	if parsed["properties"] == nil {
		t.Error("properties missing")
	}
	if parsed["items"] == nil {
		t.Error("items missing")
	}
	if parsed["enum"] == nil {
		t.Error("enum missing")
	}
	if parsed["required"] == nil {
		t.Error("required missing")
	}
	if parsed["additionalProperties"] == nil {
		t.Error("additionalProperties missing")
	}
}

func TestSchemaMarshalJSON_Empty(t *testing.T) {
	s := Schema{}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Errorf("empty schema = %q, want {}", string(b))
	}
}
