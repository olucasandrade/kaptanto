package openapi

import (
	"bytes"
	"encoding/json"
	"sort"
)

// MarshalDocument produces deterministic JSON (sorted keys, 2-space indent)
// for the given Document. Extensions are merged into the top-level object.
// OAS-01: calling this twice on the same Document yields identical bytes.
func MarshalDocument(doc *Document) ([]byte, error) {
	m := make(map[string]any)
	m["openapi"] = doc.OpenAPI
	m["info"] = doc.Info

	if doc.Paths.Len() > 0 {
		m["paths"] = doc.Paths
	}
	if doc.Components != nil {
		m["components"] = doc.Components
	}
	for k, v := range doc.Extensions {
		m[k] = v
	}

	return marshalSorted(m)
}

// marshalSorted marshals a map with sorted top-level keys and 2-space indent.
func marshalSorted(m map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",\n")
		}
		keyBytes, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.WriteString("  ")
		buf.Write(keyBytes)
		buf.WriteString(": ")

		valBytes, err := json.MarshalIndent(m[k], "  ", "  ")
		if err != nil {
			return nil, err
		}
		buf.Write(valBytes)
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}
