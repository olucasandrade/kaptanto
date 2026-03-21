// Command reporter reads benchmark NDJSON output files and writes a self-contained
// HTML report with interactive charts and a Markdown summary.
//
// Usage:
//
//	go run ./cmd/reporter --metrics=metrics.jsonl --stats=docker_stats.jsonl --output-dir=bench/results
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/kaptanto/kaptanto/bench/internal/reporter"
)

func main() {
	metricsFlag := flag.String("metrics", "metrics.jsonl", "path to metrics.jsonl NDJSON file")
	statsFlag := flag.String("stats", "docker_stats.jsonl", "path to docker_stats.jsonl NDJSON file")
	outputDirFlag := flag.String("output-dir", "bench/results", "directory to write report.html and REPORT.md")
	hardwareFlag := flag.String("hardware", "(see environment)", "hardware description for methodology section")
	flag.Parse()

	acc, err := reporter.ParseMetrics(*metricsFlag)
	if err != nil {
		log.Fatalf("parse metrics: %v", err)
	}

	statRecs, err := reporter.ParseStats(*statsFlag)
	if err != nil {
		log.Fatalf("parse stats: %v", err)
	}

	data := reporter.Aggregate(acc, statRecs)

	if err := os.MkdirAll(*outputDirFlag, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	htmlOut := filepath.Join(*outputDirFlag, "report.html")
	if err := reporter.RenderHTML(data, htmlOut, *hardwareFlag); err != nil {
		log.Fatalf("render HTML: %v", err)
	}

	mdContent := reporter.RenderMarkdown(data, "./report.html")
	mdOut := filepath.Join(*outputDirFlag, "REPORT.md")
	if err := os.WriteFile(mdOut, []byte(mdContent), 0o644); err != nil {
		log.Fatalf("write REPORT.md: %v", err)
	}

	fmt.Printf("Report written to %s\n", htmlOut)
	fmt.Printf("Markdown summary written to %s\n", mdOut)
}
