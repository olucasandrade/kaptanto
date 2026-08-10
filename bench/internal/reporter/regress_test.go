package reporter

import "testing"

func TestCompareRegression_SteadyThroughputDropFails(t *testing.T) {
	baseline := &BaselineMetrics{
		Tools: map[string]BaselineToolMetrics{
			"kaptanto": {
				Steady: BaselineScenarioMetrics{ThroughputEPS: 1000},
				Burst:  BaselineScenarioMetrics{P50us: 1_000_000},
			},
		},
	}
	current := &ReportData{
		Stats: map[string]map[string]ScenarioStats{
			"kaptanto": {
				"steady": {ThroughputEPS: 750},
				"burst":  {P50us: 1_000_000},
			},
		},
	}
	failures := CompareRegression(current, baseline, 0.20)
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	if failures[0].Metric != "throughput_eps" {
		t.Fatalf("expected throughput_eps failure, got %s", failures[0].Metric)
	}
}

func TestCompareRegression_BurstP50NotGated(t *testing.T) {
	baseline := &BaselineMetrics{
		Tools: map[string]BaselineToolMetrics{
			"kaptanto": {
				Steady: BaselineScenarioMetrics{ThroughputEPS: 1000},
				Burst:  BaselineScenarioMetrics{P50us: 1_000_000},
			},
		},
	}
	current := &ReportData{
		Stats: map[string]map[string]ScenarioStats{
			"kaptanto": {
				"steady": {ThroughputEPS: 1000},
				"burst":  {P50us: 1_300_000},
			},
		},
	}
	if failures := CompareRegression(current, baseline, 0.20); len(failures) != 0 {
		t.Fatalf("burst p50 must not gate CompareRegression, got %d failures", len(failures))
	}
	advisory := CompareBurstP50(current, baseline, 0.20)
	if len(advisory) != 1 || advisory[0].Metric != "p50_us" {
		t.Fatalf("expected advisory p50_us breach, got %v", advisory)
	}
}

func TestCompareRegression_WithinTolerancePasses(t *testing.T) {
	baseline := &BaselineMetrics{
		Tools: map[string]BaselineToolMetrics{
			"kaptanto": {
				Steady: BaselineScenarioMetrics{ThroughputEPS: 2645},
				Burst:  BaselineScenarioMetrics{P50us: 3_474_000},
			},
		},
	}
	current := &ReportData{
		Stats: map[string]map[string]ScenarioStats{
			"kaptanto": {
				"steady": {ThroughputEPS: 2500},
				"burst":  {P50us: 3_600_000},
			},
		},
	}
	failures := CompareRegression(current, baseline, RegressionTolerance)
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %d", len(failures))
	}
}
