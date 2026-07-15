package openapi

import (
	"reflect"
	"testing"
)

type testNested struct {
	Value  int    `json:"value"`
	Label  string `json:"label"`
}

type testStruct struct {
	Name     string            `json:"name"`
	Age      int               `json:"age"`
	Score    float64           `json:"score"`
	Active   bool              `json:"active"`
	Tags     []string          `json:"tags"`
	Meta     map[string]string `json:"meta"`
	Nested   testNested        `json:"nested"`
	Optional *testNested       `json:"optional,omitempty"`
	hidden   string            //nolint:unused
}

func TestTypeToSchema_AllKinds(t *testing.T) {
	s := typeToSchema(reflect.TypeOf(testStruct{}))
	if s.Type != "object" {
		t.Fatalf("type = %q, want object", s.Type)
	}

	entries := s.Properties.Entries()
	found := make(map[string]Schema)
	for _, e := range entries {
		found[e.Key] = e.Value
	}

	if found["name"].Type != "string" {
		t.Errorf("name type = %q", found["name"].Type)
	}
	if found["age"].Type != "integer" {
		t.Errorf("age type = %q", found["age"].Type)
	}
	if found["score"].Type != "number" {
		t.Errorf("score type = %q", found["score"].Type)
	}
	if found["active"].Type != "boolean" {
		t.Errorf("active type = %q", found["active"].Type)
	}
	if found["tags"].Type != "array" {
		t.Errorf("tags type = %q", found["tags"].Type)
	}
	if found["meta"].Type != "object" {
		t.Errorf("meta type = %q", found["meta"].Type)
	}
	if found["meta"].AdditionalProperties == nil {
		t.Error("meta should have additionalProperties")
	}
	if found["nested"].Type != "object" {
		t.Errorf("nested type = %q", found["nested"].Type)
	}
}

func TestTypeToSchema_Uint(t *testing.T) {
	s := typeToSchema(reflect.TypeOf(uint32(0)))
	if s.Type != "integer" {
		t.Errorf("uint32 type = %q, want integer", s.Type)
	}
}

func TestTypeToSchema_Interface(t *testing.T) {
	var iface interface{}
	s := typeToSchema(reflect.TypeOf(&iface).Elem())
	if s.Type != "" {
		t.Errorf("interface type = %q, want empty", s.Type)
	}
}
