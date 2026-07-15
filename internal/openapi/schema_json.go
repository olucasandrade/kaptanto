package openapi

import (
	"bytes"
	"encoding/json"
)

// MarshalJSON produces JSON for Schema, omitting empty Properties and other
// zero-value fields. This avoids emitting "properties": {} for leaf schemas.
func (s Schema) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true

	writeField := func(key string, val any) error {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(key)
		buf.Write(k)
		buf.WriteByte(':')
		v, err := json.Marshal(val)
		if err != nil {
			return err
		}
		buf.Write(v)
		return nil
	}

	if s.Type != "" {
		if err := writeField("type", s.Type); err != nil {
			return nil, err
		}
	}
	if s.Format != "" {
		if err := writeField("format", s.Format); err != nil {
			return nil, err
		}
	}
	if s.Description != "" {
		if err := writeField("description", s.Description); err != nil {
			return nil, err
		}
	}
	if len(s.Enum) > 0 {
		if err := writeField("enum", s.Enum); err != nil {
			return nil, err
		}
	}
	if s.Properties.Len() > 0 {
		if err := writeField("properties", s.Properties); err != nil {
			return nil, err
		}
	}
	if len(s.Required) > 0 {
		if err := writeField("required", s.Required); err != nil {
			return nil, err
		}
	}
	if s.Items != nil {
		if err := writeField("items", s.Items); err != nil {
			return nil, err
		}
	}
	if s.AdditionalProperties != nil {
		if err := writeField("additionalProperties", s.AdditionalProperties); err != nil {
			return nil, err
		}
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}
