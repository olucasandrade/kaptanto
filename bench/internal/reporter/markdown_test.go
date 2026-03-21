package reporter

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	data := &ReportData{
		Tools:     []string{"kaptanto", "debezium"},
		Scenarios: []string{"steady", "burst"},
		Stats: map[string]map[string]ScenarioStats{
			"kaptanto": {
				"steady": {ThroughputEPS: 1200.5, P50us: 10000, P95us: 20000, P99us: 30000, AvgCPUPct: 2.5, AvgRSSMB: 45.0},
				"burst":  {ThroughputEPS: 3000.0, P50us: 15000, P95us: 35000, P99us: 50000, AvgCPUPct: 8.0, AvgRSSMB: 55.0},
			},
			"debezium": {
				"steady": {ThroughputEPS: 800.0, P50us: 25000, P95us: 60000, P99us: 90000, AvgCPUPct: 12.0, AvgRSSMB: 512.0},
				"burst":  {ThroughputEPS: 2000.0, P50us: 30000, P95us: 80000, P99us: 120000, AvgCPUPct: 18.0, AvgRSSMB: 600.0},
			},
		},
		RecoverySeconds: map[string]float64{
			"kaptanto": 4.37,
			// debezium absent — should show N/A
		},
		GeneratedAt: "2026-03-21T10:00:00Z",
	}

	md := RenderMarkdown(data, "./report.html")

	// Must contain table header with "Tool"
	if !strings.Contains(md, "| Tool |") {
		t.Error("markdown does not contain '| Tool |'")
	}

	// Must contain both tool names as row entries
	if !strings.Contains(md, "| kaptanto |") {
		t.Error("markdown does not contain '| kaptanto |'")
	}
	if !strings.Contains(md, "| debezium |") {
		t.Error("markdown does not contain '| debezium |'")
	}

	// Must contain section headings
	if !strings.Contains(md, "## Latency") {
		t.Error("markdown missing '## Latency' section")
	}
	if !strings.Contains(md, "## Throughput") {
		t.Error("markdown missing '## Throughput' section")
	}
	if !strings.Contains(md, "## RSS Memory") {
		t.Error("markdown missing '## RSS Memory' section")
	}
	if !strings.Contains(md, "## Recovery Time") {
		t.Error("markdown missing '## Recovery Time' section")
	}

	// N/A for tool with zero/missing recovery
	if !strings.Contains(md, "N/A") {
		t.Error("markdown missing 'N/A' for tool with no recovery data")
	}

	// Link to report.html
	if !strings.Contains(md, "./report.html") {
		t.Error("markdown missing link to report.html")
	}

	// GeneratedAt timestamp present
	if !strings.Contains(md, "2026-03-21T10:00:00Z") {
		t.Error("markdown missing GeneratedAt timestamp")
	}
}
