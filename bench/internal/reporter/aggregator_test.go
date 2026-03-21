package reporter

import (
	"testing"
	"time"
)

// TestPercentile_KnownSlice verifies nearest-rank formula on [10,20,30,40,50].
func TestPercentile_KnownSlice(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50}
	// N=5, p50: ceil(50/100*5)-1 = ceil(2.5)-1 = 3-1 = 2 → sorted[2]=30
	if got := percentile(sorted, 50); got != 30 {
		t.Errorf("p50 = %d, want 30", got)
	}
	// p95: ceil(95/100*5)-1 = ceil(4.75)-1 = 5-1 = 4 → sorted[4]=50
	if got := percentile(sorted, 95); got != 50 {
		t.Errorf("p95 = %d, want 50", got)
	}
	// p99: ceil(99/100*5)-1 = ceil(4.95)-1 = 5-1 = 4 → sorted[4]=50
	if got := percentile(sorted, 99); got != 50 {
		t.Errorf("p99 = %d, want 50", got)
	}
}

// TestPercentile_Empty verifies zero return and no panic on empty slice.
func TestPercentile_Empty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("percentile(nil, 50) = %d, want 0", got)
	}
	if got := percentile([]int64{}, 99); got != 0 {
		t.Errorf("percentile([], 99) = %d, want 0", got)
	}
}

// TestPercentile_SingleElement verifies all percentiles return the only element.
func TestPercentile_SingleElement(t *testing.T) {
	sorted := []int64{42}
	for _, p := range []float64{50, 95, 99} {
		if got := percentile(sorted, p); got != 42 {
			t.Errorf("percentile([42], %.0f) = %d, want 42", p, got)
		}
	}
}

// TestAggregate_Throughput verifies throughput = count / duration_seconds.
func TestAggregate_Throughput(t *testing.T) {
	t0 := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	t5 := t0.Add(5 * time.Second)

	k := key{tool: "kaptanto", scenario: "steady"}
	acc := &Accumulator{
		Latencies:       map[key][]int64{k: {100, 200, 300, 400, 500}},
		EventCounts:     map[key]int64{k: 5},
		MinTS:           map[key]time.Time{k: t0},
		MaxTS:           map[key]time.Time{k: t5},
		ScenarioWindows: map[string]ScenarioWindow{},
		RecoveryTime:    map[string]float64{},
	}

	rd := Aggregate(acc, nil)
	st := rd.Stats["kaptanto"]["steady"]
	if st.ThroughputEPS != 1.0 {
		t.Errorf("ThroughputEPS = %v, want 1.0", st.ThroughputEPS)
	}
}

// TestAggregate_ThroughputZeroCount verifies no divide-by-zero when count=0.
func TestAggregate_ThroughputZeroCount(t *testing.T) {
	k := key{tool: "kaptanto", scenario: "idle"}
	acc := &Accumulator{
		Latencies:       map[key][]int64{k: {}},
		EventCounts:     map[key]int64{k: 0},
		MinTS:           map[key]time.Time{},
		MaxTS:           map[key]time.Time{},
		ScenarioWindows: map[string]ScenarioWindow{},
		RecoveryTime:    map[string]float64{},
	}

	rd := Aggregate(acc, nil)
	st := rd.Stats["kaptanto"]["idle"]
	if st.ThroughputEPS != 0 {
		t.Errorf("ThroughputEPS = %v, want 0", st.ThroughputEPS)
	}
}

// TestAggregate_StatAssignment verifies StatRecord within window is assigned;
// records outside all windows are ignored.
func TestAggregate_StatAssignment(t *testing.T) {
	t0 := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(60 * time.Second)
	tInside := t0.Add(30 * time.Second)
	tOutside := t0.Add(120 * time.Second)

	k := key{tool: "kaptanto", scenario: "steady"}
	acc := &Accumulator{
		Latencies:   map[key][]int64{k: {500}},
		EventCounts: map[key]int64{k: 1},
		MinTS:       map[key]time.Time{k: t0},
		MaxTS:       map[key]time.Time{k: t1},
		ScenarioWindows: map[string]ScenarioWindow{
			"steady": {Start: t0, End: t1},
		},
		RecoveryTime: map[string]float64{},
	}

	stats := []StatRecord{
		{Container: "kaptanto", TS: tInside, CPUPCT: 10.0, VmRSSKB: 1024},
		{Container: "kaptanto", TS: tOutside, CPUPCT: 99.0, VmRSSKB: 999999},
	}

	rd := Aggregate(acc, stats)
	st := rd.Stats["kaptanto"]["steady"]

	// Only the record inside the window should count.
	if st.AvgCPUPct != 10.0 {
		t.Errorf("AvgCPUPct = %v, want 10.0", st.AvgCPUPct)
	}
	wantRSSMB := float64(1024) / 1024.0
	if st.AvgRSSMB != wantRSSMB {
		t.Errorf("AvgRSSMB = %v, want %v", st.AvgRSSMB, wantRSSMB)
	}
}

// TestAggregate_MeanCPU verifies mean CPU across 3 samples.
func TestAggregate_MeanCPU(t *testing.T) {
	t0 := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(60 * time.Second)

	k := key{tool: "kaptanto", scenario: "steady"}
	acc := &Accumulator{
		Latencies:   map[key][]int64{k: {100}},
		EventCounts: map[key]int64{k: 1},
		MinTS:       map[key]time.Time{k: t0},
		MaxTS:       map[key]time.Time{k: t1},
		ScenarioWindows: map[string]ScenarioWindow{
			"steady": {Start: t0, End: t1},
		},
		RecoveryTime: map[string]float64{},
	}

	stats := []StatRecord{
		{Container: "kaptanto", TS: t0.Add(10 * time.Second), CPUPCT: 10.0, VmRSSKB: 0},
		{Container: "kaptanto", TS: t0.Add(20 * time.Second), CPUPCT: 20.0, VmRSSKB: 0},
		{Container: "kaptanto", TS: t0.Add(30 * time.Second), CPUPCT: 30.0, VmRSSKB: 0},
	}

	rd := Aggregate(acc, stats)
	st := rd.Stats["kaptanto"]["steady"]
	if st.AvgCPUPct != 20.0 {
		t.Errorf("AvgCPUPct = %v, want 20.0", st.AvgCPUPct)
	}
}

// TestAggregate_Recovery verifies recovery times pass through correctly.
func TestAggregate_Recovery(t *testing.T) {
	k := key{tool: "kaptanto", scenario: "crash-recovery"}
	acc := &Accumulator{
		Latencies:       map[key][]int64{k: {100}},
		EventCounts:     map[key]int64{k: 1},
		MinTS:           map[key]time.Time{k: time.Now()},
		MaxTS:           map[key]time.Time{k: time.Now().Add(time.Second)},
		ScenarioWindows: map[string]ScenarioWindow{},
		RecoveryTime:    map[string]float64{"kaptanto": 4.37},
	}

	rd := Aggregate(acc, nil)
	if got := rd.RecoverySeconds["kaptanto"]; got != 4.37 {
		t.Errorf("RecoverySeconds[kaptanto] = %v, want 4.37", got)
	}
	// Tool not in recovery map gets zero value.
	if got := rd.RecoverySeconds["debezium"]; got != 0 {
		t.Errorf("RecoverySeconds[debezium] = %v, want 0", got)
	}
}

// TestAggregate_CanonicalOrder verifies tool and scenario ordering.
func TestAggregate_CanonicalOrder(t *testing.T) {
	k1 := key{tool: "debezium", scenario: "burst"}
	k2 := key{tool: "kaptanto", scenario: "steady"}
	k3 := key{tool: "sequin", scenario: "idle"}

	t0 := time.Now()
	t1 := t0.Add(time.Second)

	acc := &Accumulator{
		Latencies:       map[key][]int64{k1: {100}, k2: {200}, k3: {300}},
		EventCounts:     map[key]int64{k1: 1, k2: 1, k3: 1},
		MinTS:           map[key]time.Time{k1: t0, k2: t0, k3: t0},
		MaxTS:           map[key]time.Time{k1: t1, k2: t1, k3: t1},
		ScenarioWindows: map[string]ScenarioWindow{},
		RecoveryTime:    map[string]float64{},
	}

	rd := Aggregate(acc, nil)

	// Tools must appear in canonical order: kaptanto before debezium before sequin.
	toolIdx := map[string]int{}
	for i, t := range rd.Tools {
		toolIdx[t] = i
	}
	if toolIdx["kaptanto"] >= toolIdx["debezium"] {
		t.Errorf("kaptanto should come before debezium in Tools slice")
	}
	if toolIdx["debezium"] >= toolIdx["sequin"] {
		t.Errorf("debezium should come before sequin in Tools slice")
	}

	// Scenarios: steady before burst before idle.
	scenIdx := map[string]int{}
	for i, s := range rd.Scenarios {
		scenIdx[s] = i
	}
	if scenIdx["steady"] >= scenIdx["burst"] {
		t.Errorf("steady should come before burst in Scenarios slice")
	}
	if scenIdx["burst"] >= scenIdx["idle"] {
		t.Errorf("burst should come before idle in Scenarios slice")
	}
}
