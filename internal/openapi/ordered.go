package openapi

import (
	"bytes"
	"encoding/json"
	"sort"
)

// orderedMap is a generic map that marshals JSON keys in sorted order for
// deterministic output (OAS-01). It supports both insertion via Set and
// iteration via Entries.
type orderedMap[V any] struct {
	entries []entry[V]
	idx     map[string]int
}

type entry[V any] struct {
	Key   string
	Value V
}

// Set adds or replaces a key. Insertion order is irrelevant; keys are always
// marshaled in lexicographic order.
func (m *orderedMap[V]) Set(key string, val V) {
	if m.idx == nil {
		m.idx = make(map[string]int)
	}
	if i, ok := m.idx[key]; ok {
		m.entries[i].Value = val
		return
	}
	m.idx[key] = len(m.entries)
	m.entries = append(m.entries, entry[V]{Key: key, Value: val})
}

// Len returns the number of entries.
func (m *orderedMap[V]) Len() int {
	if m == nil {
		return 0
	}
	return len(m.entries)
}

// Entries returns all entries sorted by key.
func (m *orderedMap[V]) Entries() []entry[V] {
	sorted := make([]entry[V], len(m.entries))
	copy(sorted, m.entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	return sorted
}

// MarshalJSON produces a JSON object with keys in sorted order.
func (m orderedMap[V]) MarshalJSON() ([]byte, error) {
	if len(m.entries) == 0 {
		return []byte("{}"), nil
	}
	sorted := m.Entries()
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range sorted {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(e.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := json.Marshal(e.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
