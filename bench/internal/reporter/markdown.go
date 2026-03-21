package reporter

import (
	"fmt"
	"strings"
)

// mdTable builds a GitHub-Flavored Markdown table from headers and rows.
func mdTable(headers []string, rows [][]string) string {
	var sb strings.Builder
	sb.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	sb.WriteString("|" + strings.Repeat(" --- |", len(headers)) + "\n")
	for _, row := range rows {
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	return sb.String()
}

// RenderMarkdown returns a Markdown string summarising the report data.
// The caller is responsible for writing the returned string to a file.
// htmlPath is the relative path to the HTML report (used in the link).
func RenderMarkdown(data *ReportData, htmlPath string) string {
	var sb strings.Builder

	sb.WriteString("# Kaptanto Benchmark Report\n\n")
	sb.WriteString("Generated: " + data.GeneratedAt + "\n\n")
	sb.WriteString("[View interactive report](" + htmlPath + ")\n\n")

	// Latency table: p50 / p95 / p99 in milliseconds.
	sb.WriteString("## Latency\n\n")
	latHeaders := append([]string{"Tool"}, data.Scenarios...)
	var latRows [][]string
	for _, tool := range data.Tools {
		row := []string{tool}
		for _, scen := range data.Scenarios {
			ss := data.Stats[tool][scen]
			p50ms := float64(ss.P50us) / 1000.0
			p95ms := float64(ss.P95us) / 1000.0
			p99ms := float64(ss.P99us) / 1000.0
			row = append(row, fmt.Sprintf("%.2f/%.2f/%.2f ms", p50ms, p95ms, p99ms))
		}
		latRows = append(latRows, row)
	}
	sb.WriteString(mdTable(latHeaders, latRows))
	sb.WriteString("\n")

	// Throughput table: events per second.
	sb.WriteString("## Throughput\n\n")
	tpHeaders := append([]string{"Tool"}, data.Scenarios...)
	var tpRows [][]string
	for _, tool := range data.Tools {
		row := []string{tool}
		for _, scen := range data.Scenarios {
			ss := data.Stats[tool][scen]
			row = append(row, fmt.Sprintf("%.0f eps", ss.ThroughputEPS))
		}
		tpRows = append(tpRows, row)
	}
	sb.WriteString(mdTable(tpHeaders, tpRows))
	sb.WriteString("\n")

	// RSS table: average RSS in MB.
	sb.WriteString("## RSS Memory\n\n")
	rssHeaders := append([]string{"Tool"}, data.Scenarios...)
	var rssRows [][]string
	for _, tool := range data.Tools {
		row := []string{tool}
		for _, scen := range data.Scenarios {
			ss := data.Stats[tool][scen]
			row = append(row, fmt.Sprintf("%.1f MB", ss.AvgRSSMB))
		}
		rssRows = append(rssRows, row)
	}
	sb.WriteString(mdTable(rssHeaders, rssRows))
	sb.WriteString("\n")

	// Recovery table: seconds per tool, or "N/A" if absent/zero.
	sb.WriteString("## Recovery Time\n\n")
	var recRows [][]string
	for _, tool := range data.Tools {
		secs := data.RecoverySeconds[tool]
		var val string
		if secs == 0 {
			val = "N/A"
		} else {
			val = fmt.Sprintf("%.2f s", secs)
		}
		recRows = append(recRows, []string{tool, val})
	}
	sb.WriteString(mdTable([]string{"Tool", "Recovery (s)"}, recRows))
	sb.WriteString("\n")

	return sb.String()
}
