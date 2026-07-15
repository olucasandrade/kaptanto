package openapi

import (
	"encoding/json"
	"testing"
)

func TestOrderedMap_Set_Update(t *testing.T) {
	var m orderedMap[string]
	m.Set("a", "one")
	m.Set("b", "two")
	m.Set("a", "updated")

	if m.Len() != 2 {
		t.Fatalf("len = %d, want 2", m.Len())
	}

	entries := m.Entries()
	for _, e := range entries {
		if e.Key == "a" && e.Value != "updated" {
			t.Errorf("a = %q, want 'updated'", e.Value)
		}
	}
}

func TestOrderedMap_SortedKeys(t *testing.T) {
	var m orderedMap[int]
	m.Set("z", 1)
	m.Set("a", 2)
	m.Set("m", 3)

	entries := m.Entries()
	if entries[0].Key != "a" || entries[1].Key != "m" || entries[2].Key != "z" {
		t.Errorf("entries not sorted: %v", entries)
	}
}

func TestOrderedMap_MarshalJSON_Empty(t *testing.T) {
	var m orderedMap[string]
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Errorf("empty map = %q, want {}", string(b))
	}
}

func TestOrderedMap_MarshalJSON_Sorted(t *testing.T) {
	var m orderedMap[string]
	m.Set("c", "3")
	m.Set("a", "1")
	m.Set("b", "2")

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"1","b":"2","c":"3"}`
	if string(b) != want {
		t.Errorf("got %s, want %s", string(b), want)
	}
}


