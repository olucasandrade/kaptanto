package scenarios

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Test 1: ScenarioDef for "steady" has LoadgenArgs containing "--mode" "steady" and "--rate" "10000"
func TestSteadyScenarioDef(t *testing.T) {
	var found *ScenarioDef
	for i := range Scenarios {
		if Scenarios[i].Name == "steady" {
			found = &Scenarios[i]
			break
		}
	}
	if found == nil {
		t.Fatal("steady scenario not found in Scenarios slice")
	}

	hasMode := false
	hasSteady := false
	hasRate := false
	has10000 := false

	for i, arg := range found.LoadgenArgs {
		if arg == "--mode" {
			hasMode = true
			if i+1 < len(found.LoadgenArgs) && found.LoadgenArgs[i+1] == "steady" {
				hasSteady = true
			}
		}
		if arg == "--rate" {
			hasRate = true
			if i+1 < len(found.LoadgenArgs) && found.LoadgenArgs[i+1] == "10000" {
				has10000 = true
			}
		}
	}

	if !hasMode || !hasSteady {
		t.Errorf("steady scenario missing --mode steady, LoadgenArgs=%v", found.LoadgenArgs)
	}
	if !hasRate || !has10000 {
		t.Errorf("steady scenario missing --rate 10000, LoadgenArgs=%v", found.LoadgenArgs)
	}
}

// Test 2: setScenarioTag(url, "steady") sends POST to /scenario?name=steady and returns nil on 200
func TestSetScenarioTagSuccess(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/scenario" {
			t.Errorf("expected /scenario, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("name") != "steady" {
			t.Errorf("expected name=steady, got %s", r.URL.Query().Get("name"))
		}
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	r := &Runner{CollectorURL: ts.URL}
	err := r.setScenarioTag(nil, "steady") //nolint:staticcheck
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
}

// Test 3: setScenarioTag returns error on non-200 or connection failure
func TestSetScenarioTagError(t *testing.T) {
	// Non-200 response
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	r := &Runner{CollectorURL: ts.URL}
	err := r.setScenarioTag(nil, "steady") //nolint:staticcheck
	if err == nil {
		t.Error("expected error on non-200 response")
	}

	// Connection failure (closed server)
	ts.Close()
	err = r.setScenarioTag(nil, "steady") //nolint:staticcheck
	if err == nil {
		t.Error("expected error on connection failure")
	}
}

// Test 4: writeMarker writes a JSON line with scenario_event, scenario, and ts fields to the output file
func TestWriteMarker(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{OutputDir: dir}

	err := r.writeMarker("steady", "start")
	if err != nil {
		t.Fatalf("writeMarker error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "metrics.jsonl"))
	if err != nil {
		t.Fatalf("failed to read metrics.jsonl: %v", err)
	}

	var rec map[string]interface{}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("failed to unmarshal JSON line: %v", err)
	}

	if rec["scenario_event"] != "start" {
		t.Errorf("expected scenario_event=start, got %v", rec["scenario_event"])
	}
	if rec["scenario"] != "steady" {
		t.Errorf("expected scenario=steady, got %v", rec["scenario"])
	}
	if _, ok := rec["ts"]; !ok {
		t.Error("expected ts field in JSON line")
	}
}

// Test 5: buildLoadgenCmd returns exec.Cmd with correct binary path and args from ScenarioDef
func TestBuildLoadgenCmd(t *testing.T) {
	r := &Runner{
		LoadgenBin: "/usr/local/bin/loadgen",
		DSN:        "postgres://bench:bench@localhost:5432/bench",
	}
	s := ScenarioDef{
		Name:        "steady",
		LoadgenArgs: []string{"--mode", "steady", "--rate", "10000", "--duration", "60s"},
	}

	cmd := r.buildLoadgenCmd(nil, s) //nolint:staticcheck

	if cmd.Path != "/usr/local/bin/loadgen" {
		t.Errorf("expected path /usr/local/bin/loadgen, got %s", cmd.Path)
	}

	// Args[0] is the binary itself in exec.Cmd
	expectedArgs := []string{"/usr/local/bin/loadgen", "--dsn", r.DSN, "--mode", "steady", "--rate", "10000", "--duration", "60s"}
	if len(cmd.Args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(cmd.Args), cmd.Args)
	}
	for i, arg := range expectedArgs {
		if cmd.Args[i] != arg {
			t.Errorf("arg[%d]: expected %q, got %q", i, arg, cmd.Args[i])
		}
	}
}

// Test 6: Scenarios slice has exactly 5 entries with names: steady, burst, large-batch, crash-recovery, idle
func TestScenariosCount(t *testing.T) {
	if len(Scenarios) != 5 {
		t.Fatalf("expected 5 scenarios, got %d", len(Scenarios))
	}
}

func TestScenarioDefs(t *testing.T) {
	expectedNames := []string{"steady", "burst", "large-batch", "crash-recovery", "idle"}
	for i, name := range expectedNames {
		if Scenarios[i].Name != name {
			t.Errorf("Scenarios[%d].Name: expected %q, got %q", i, name, Scenarios[i].Name)
		}
	}
}
