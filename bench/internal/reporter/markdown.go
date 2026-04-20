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

	// Executive Summary: best throughput and latency across all scenarios per tool.
	sb.WriteString("## Executive Summary\n\n")
	summaryHeaders := []string{"Tool", "Peak Throughput (eps)", "p50 Latency (ms)", "p95 Latency (ms)", "Recovery (s)", "Infrastructure"}
	infraMap := map[string]string{
		"kaptanto":      "1 binary (Go, ~15 MB)",
		"kaptanto-rust": "1 binary (Go+Rust FFI, ~15 MB)",
		"debezium":      "JVM + config files",
		"sequin":        "Elixir + Redis + PG",
		"peerdb":        "4 Go services + Temporal + Kafka + PG",
	}
	var summaryRows [][]string
	for _, tool := range data.Tools {
		var bestThroughput float64
		var bestP50, bestP95 int64
		first := true
		for _, scen := range data.Scenarios {
			ss := data.Stats[tool][scen]
			if ss.ThroughputEPS > bestThroughput {
				bestThroughput = ss.ThroughputEPS
			}
			if first || (ss.P50us > 0 && ss.P50us < bestP50) {
				if ss.P50us > 0 {
					bestP50 = ss.P50us
					bestP95 = ss.P95us
					first = false
				}
			}
		}
		rec := data.RecoverySeconds[tool]
		recStr := "N/A"
		if rec > 0 {
			recStr = fmt.Sprintf("%.1f", rec)
		}
		infra := infraMap[tool]
		if infra == "" {
			infra = "-"
		}
		summaryRows = append(summaryRows, []string{
			tool,
			fmt.Sprintf("%.0f", bestThroughput),
			fmt.Sprintf("%.1f", float64(bestP50)/1000.0),
			fmt.Sprintf("%.1f", float64(bestP95)/1000.0),
			recStr,
			infra,
		})
	}
	sb.WriteString(mdTable(summaryHeaders, summaryRows))
	sb.WriteString("\n")

	// Latency table: p50 / p95 / p99 in milliseconds.
	sb.WriteString("## Latency (p50 / p95 / p99 ms)\n\n")
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
