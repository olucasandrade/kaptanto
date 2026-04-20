// Chart.js 4.5.0 UMD: https://cdn.jsdelivr.net/npm/chart.js@4.5.0/dist/chart.umd.min.js (~208341 bytes)
// Asset location: bench/internal/reporter/assets/chart.umd.min.js
// Note: go:embed paths cannot contain ".." so the asset lives adjacent to this package.

package reporter

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"time"
)

//go:embed assets/chart.umd.min.js
var chartJSContent string

// toolColors provides a cycling color palette for up to 4 tools.
var toolColors = []string{"#FF6384", "#9B59B6", "#36A2EB", "#FFCE56", "#4BC0C0"}

// ChartDataset is the per-tool dataset shape expected by Chart.js.
type ChartDataset struct {
	Label           string    `json:"label"`
	Data            []float64 `json:"data"`
	BackgroundColor string    `json:"backgroundColor"`
	BorderColor     string    `json:"borderColor"`
	BorderWidth     int       `json:"borderWidth"`
}

// ChartData is the top-level data object passed to new Chart(...).
type ChartData struct {
	Labels   []string       `json:"labels"`   // scenario names
	Datasets []ChartDataset `json:"datasets"` // one per tool
}

// buildChart constructs a grouped bar ChartData for a given metric extracted
// from each (tool, scenario) pair using extractor, then returns it as
// template.JS so html/template will not HTML-escape the JSON.
func buildChart(data *ReportData, extractor func(ScenarioStats) float64) template.JS {
	cd := ChartData{Labels: data.Scenarios}
	for i, tool := range data.Tools {
		color := toolColors[i%len(toolColors)]
		ds := ChartDataset{
			Label:           tool,
			BackgroundColor: color + "33", // ~20% opacity
			BorderColor:     color,
			BorderWidth:     1,
		}
		for _, scen := range data.Scenarios {
			ss := data.Stats[tool][scen]
			ds.Data = append(ds.Data, extractor(ss))
		}
		cd.Datasets = append(cd.Datasets, ds)
	}
	b, _ := json.Marshal(cd)
	return template.JS(b) //nolint:gosec // chart data is computed from trusted internal structs
}

// buildRecoveryChart constructs a bar chart where each dataset has a single
// bar representing one tool's recovery time. Missing tools get value 0.
func buildRecoveryChart(data *ReportData) template.JS {
	labels := data.Tools
	cd := ChartData{Labels: labels}
	for i, tool := range data.Tools {
		color := toolColors[i%len(toolColors)]
		val := data.RecoverySeconds[tool] // 0 if absent
		ds := ChartDataset{
			Label:           tool,
			BackgroundColor: color + "33",
			BorderColor:     color,
			BorderWidth:     1,
			Data:            []float64{val},
		}
		cd.Datasets = append(cd.Datasets, ds)
	}
	// Override labels to a single element matching the metric name
	cd.Labels = []string{"Recovery Time (s)"}
	b, _ := json.Marshal(cd)
	return template.JS(b) //nolint:gosec // chart data is computed from trusted internal structs
}

// htmlTemplate is the self-contained HTML report template.
// All JavaScript content uses template.JS to prevent html/template auto-escaping.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Kaptanto Benchmark Report</title>
<style>
body { font-family: sans-serif; max-width: 1200px; margin: 0 auto; padding: 1rem 2rem; color: #333; }
h1 { border-bottom: 2px solid #eee; padding-bottom: 0.5rem; }
h2 { margin-top: 2rem; color: #444; }
.chart-container { position: relative; height: 400px; margin-bottom: 2rem; }
table { border-collapse: collapse; width: 100%; margin-bottom: 1rem; }
th, td { border: 1px solid #ddd; padding: 0.5rem 0.75rem; text-align: left; font-size: 0.9rem; }
th { background: #f5f5f5; font-weight: 600; }
tr:nth-child(even) { background: #fafafa; }
.meta { color: #888; font-size: 0.85rem; margin-top: 0.25rem; }
.section { margin-bottom: 3rem; }
</style>
</head>
<body>
<h1>Kaptanto Benchmark Report</h1>
<p class="meta">Generated: {{.GeneratedAt}}</p>

<script>{{.ChartJS}}</script>

<div class="section">
<h2>Summary Table</h2>
<table>
<thead><tr><th>Tool</th><th>Scenario</th><th>p50 (ms)</th><th>Throughput (eps)</th></tr></thead>
<tbody>
{{range $tool := .Tools}}{{range $scen := $.Scenarios}}
<tr>
  <td>{{$tool}}</td>
  <td>{{$scen}}</td>
  <td>{{index (index $.Stats $tool) $scen | p50ms}}</td>
  <td>{{index (index $.Stats $tool) $scen | throughput}}</td>
</tr>
{{end}}{{end}}
</tbody>
</table>
</div>

<div class="section">
<h2>Throughput (events/sec)</h2>
<div class="chart-container"><canvas id="chart-throughput"></canvas></div>
<script>
new Chart(document.getElementById('chart-throughput'), {
  type: 'bar',
  data: {{.ThroughputChart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>Latency p50 (ms)</h2>
<div class="chart-container"><canvas id="chart-p50"></canvas></div>
<script>
new Chart(document.getElementById('chart-p50'), {
  type: 'bar',
  data: {{.P50Chart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>Latency p95 (ms)</h2>
<div class="chart-container"><canvas id="chart-p95"></canvas></div>
<script>
new Chart(document.getElementById('chart-p95'), {
  type: 'bar',
  data: {{.P95Chart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>Latency p99 (ms)</h2>
<div class="chart-container"><canvas id="chart-p99"></canvas></div>
<script>
new Chart(document.getElementById('chart-p99'), {
  type: 'bar',
  data: {{.P99Chart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>CPU Usage (%)</h2>
<div class="chart-container"><canvas id="chart-cpu"></canvas></div>
<script>
new Chart(document.getElementById('chart-cpu'), {
  type: 'bar',
  data: {{.CPUChart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>RSS Memory (MB)</h2>
<div class="chart-container"><canvas id="chart-rss"></canvas></div>
<script>
new Chart(document.getElementById('chart-rss'), {
  type: 'bar',
  data: {{.RSSChart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>Recovery Time (seconds)</h2>
<div class="chart-container"><canvas id="chart-recovery"></canvas></div>
<script>
new Chart(document.getElementById('chart-recovery'), {
  type: 'bar',
  data: {{.RecoveryChart}},
  options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } } }
});
</script>
</div>

<div class="section">
<h2>Methodology</h2>
<h3>Tool Versions</h3>
<ul>
  <li><strong>Kaptanto</strong> — built from source (this repository)</li>
  <li><strong>Debezium Server</strong> — 3.4.2.Final</li>
  <li><strong>Sequin</strong> — v0.14.6</li>
  <li><strong>PeerDB</strong> — v0.36.12</li>
</ul>
<h3>Hardware</h3>
<p>{{.Hardware}}</p>
<h3>Scenario Definitions</h3>
<ul>
  <li><strong>steady</strong> — Sustained constant-rate insert workload (30 s warmup, 5 min measurement window). Measures steady-state throughput and latency distribution.</li>
  <li><strong>burst</strong> — Short high-rate burst of inserts followed by idle period. Measures peak throughput and latency under load spike.</li>
  <li><strong>large-batch</strong> — Single transaction inserting 100 000 rows. Measures how each tool handles a large transaction commit.</li>
  <li><strong>crash-recovery</strong> — Container is killed mid-run and restarted; recovery time is measured as seconds until all expected events are delivered.</li>
  <li><strong>idle</strong> — No inserts. Measures baseline CPU and memory at rest.</li>
</ul>
<h3>Measurement Approach</h3>
<p>
  End-to-end latency is measured as <code>receive_ts − bench_ts</code>, where
  <code>bench_ts</code> is the wall-clock time the row was inserted by the load
  generator and <code>receive_ts</code> is the wall-clock time the event was
  received by the collector. Both timestamps are set on the same host using
  <code>time.Now()</code>; clock drift is not corrected.
</p>
<p>
  CPU% and RSS are sampled from <code>/proc/1/status</code> (VmRSS field) inside
  each container via <code>docker stats</code>, polled every 2 seconds. Reported
  values are arithmetic means over the scenario measurement window.
</p>
<h3>Maxwell's Daemon Exclusion</h3>
<p>
  Maxwell's Daemon was excluded from this benchmark because it supports MySQL only
  and does not implement Postgres logical replication (confirmed in maintainer
  issue #434). All other tools in this benchmark (Debezium, Sequin, PeerDB,
  Kaptanto) support Postgres CDC.
</p>
</div>

</body>
</html>`

// RenderHTML populates the template.JS fields of data, executes the HTML
// template, and writes the result to outPath.
// hardware is passed verbatim into the methodology section.
func RenderHTML(data *ReportData, outPath string, hardware string) error {
	data.ChartJS = template.JS(chartJSContent) //nolint:gosec // trusted committed file
	data.Hardware = hardware
	data.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	data.ThroughputChart = buildChart(data, func(ss ScenarioStats) float64 { return ss.ThroughputEPS })
	data.P50Chart = buildChart(data, func(ss ScenarioStats) float64 { return float64(ss.P50us) / 1000.0 })
	data.P95Chart = buildChart(data, func(ss ScenarioStats) float64 { return float64(ss.P95us) / 1000.0 })
	data.P99Chart = buildChart(data, func(ss ScenarioStats) float64 { return float64(ss.P99us) / 1000.0 })
	data.CPUChart = buildChart(data, func(ss ScenarioStats) float64 { return ss.AvgCPUPct })
	data.RSSChart = buildChart(data, func(ss ScenarioStats) float64 { return ss.AvgRSSMB })
	data.RecoveryChart = buildRecoveryChart(data)

	funcMap := template.FuncMap{
		"p50ms": func(ss ScenarioStats) string {
			return formatFloat(float64(ss.P50us)/1000.0, 2)
		},
		"throughput": func(ss ScenarioStats) string {
			return formatFloat(ss.ThroughputEPS, 0)
		},
	}

	tmpl, err := template.New("report").Funcs(funcMap).Parse(htmlTemplate)
	if err != nil {
		return err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}

// formatFloat formats f with prec decimal places.
func formatFloat(f float64, prec int) string {
	return fmt.Sprintf("%."+fmt.Sprintf("%d", prec)+"f", f)
}
