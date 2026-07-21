package openapi

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func TestGoldenSpec(t *testing.T) {
	opts := GenerateOptions{
		Output:    "sse",
		AuthToken: true,
		Actions: []ActionMeta{
			{
				Name:       "notify-slack",
				Type:       "slack",
				Tables:     []string{"public.orders"},
				Operations: []string{"insert", "update"},
				ParamNames: []string{"channel"},
			},
		},
	}

	doc := Generate(opts)
	got, err := MarshalDocument(doc)
	if err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "openapi.golden.json")

	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		t.Log("golden file updated")
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v (run with -update to generate)", err)
	}

	if string(got) != string(want) {
		t.Fatalf("OAS-01: spec differs from golden file.\nRun: go test ./internal/openapi/ -update\n\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Regenerate a second time to confirm determinism
	doc2 := Generate(opts)
	got2, err := MarshalDocument(doc2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(got2) {
		t.Fatal("OAS-01: regenerating the spec produces different bytes")
	}
}
