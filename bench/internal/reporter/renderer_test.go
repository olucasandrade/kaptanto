package reporter

import (
	"os"
	"strings"
	"testing"
)

func TestRenderHTML(t *testing.T) {
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
		},
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), "report-*.html")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmpFile.Close()
	outPath := tmpFile.Name()

	if err := RenderHTML(data, outPath, "Test Hardware (2 vCPU, 4 GB RAM)"); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	html := string(content)

	// Must start with DOCTYPE and end with </html>
	if !strings.HasPrefix(strings.TrimSpace(html), "<!DOCTYPE html") {
		t.Error("HTML does not start with <!DOCTYPE html")
	}
	if !strings.HasSuffix(strings.TrimSpace(html), "</html>") {
		t.Error("HTML does not end with </html>")
	}

	// Must contain unescaped <canvas> tags (not &lt;canvas)
	if strings.Contains(html, "&lt;canvas") {
		t.Error("HTML contains escaped &lt;canvas — template.JS not applied correctly")
	}
	if !strings.Contains(html, "<canvas") {
		t.Error("HTML missing <canvas elements")
	}

	// Must contain 7 canvas elements (throughput, p50, p95, p99, cpu, rss, recovery)
	canvasCount := strings.Count(html, "<canvas")
	if canvasCount < 7 {
		t.Errorf("expected at least 7 <canvas elements, got %d", canvasCount)
	}

	// Must contain the Chart.js library (embedded, not CDN)
	if !strings.Contains(html, "chart.umd") || !strings.Contains(html, "Chart") {
		t.Error("HTML does not appear to contain Chart.js library")
	}

	// Must NOT contain any CDN URLs
	cdnPatterns := []string{
		"cdn.jsdelivr.net",
		"cdn.cloudflare.com",
		"unpkg.com",
		"cdn.jsdelivr",
		"cdnjs.cloudflare.com",
	}
	for _, cdn := range cdnPatterns {
		if strings.Contains(html, cdn) {
			t.Errorf("HTML contains CDN URL: %s (report must be self-contained)", cdn)
		}
	}

	// Must contain methodology section with Maxwell's Daemon
	if !strings.Contains(html, "Maxwell") {
		t.Error("HTML missing Maxwell's Daemon exclusion in methodology section")
	}

	// Must contain both tool names
	if !strings.Contains(html, "kaptanto") {
		t.Error("HTML missing tool name 'kaptanto'")
	}
	if !strings.Contains(html, "debezium") {
		t.Error("HTML missing tool name 'debezium'")
	}

	// Chart data must not be HTML-escaped (check for &lt; in script blocks)
	if strings.Contains(html, "\\u003c") {
		t.Error("HTML contains \\u003c in chart data — JSON is being HTML-escaped; use template.JS")
	}
}
