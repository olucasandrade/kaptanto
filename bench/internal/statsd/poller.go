// Package statsd polls Docker container resource usage (CPU%, VmRSS) and
// appends one JSON line per container per tick to an NDJSON output file.
package statsd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StatRecord is one measurement for one container at one point in time.
// JSON field names are authoritative: Phase 13's report generator reads
// "vmrss_kb" and "cpu_pct" by name from docker_stats.jsonl.
type StatRecord struct {
	Container string    `json:"container"`
	TS        time.Time `json:"ts"`
	CPUPCT    float64   `json:"cpu_pct"`
	VmRSSKB   int64     `json:"vmrss_kb"`
}

// containerStats holds both CPU and memory for one container poll.
type containerStats struct {
	CPUPCT  float64
	VmRSSKB int64
}

// readContainerStats calls docker stats --no-stream once and returns both
// CPU% and memory usage in kibibytes. Uses the Docker stats API (works on
// any OS via the Docker socket; does not require /proc or pid:host).
func readContainerStats(containerName string) (containerStats, error) {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}", containerName).Output()
	if err != nil {
		return containerStats{}, fmt.Errorf("readContainerStats: docker stats %s: %w", containerName, err)
	}
	var row struct {
		CPUPerc  string `json:"CPUPerc"`
		MemUsage string `json:"MemUsage"`
	}
	if err := json.Unmarshal(out, &row); err != nil {
		return containerStats{}, fmt.Errorf("readContainerStats: unmarshal: %w (output: %q)", err, out)
	}
	cpu, err := parseCPUPct(row.CPUPerc)
	if err != nil {
		return containerStats{}, fmt.Errorf("readContainerStats: cpu: %w", err)
	}
	memKB, err := parseMemUsage(row.MemUsage)
	if err != nil {
		return containerStats{}, fmt.Errorf("readContainerStats: mem: %w", err)
	}
	return containerStats{CPUPCT: cpu, VmRSSKB: memKB}, nil
}

// parseCPUPct converts a docker stats CPUPerc string (e.g. "0.13%") to a
// float64. The trailing "%" is optional — both "0.13%" and "0.13" are accepted.
// Returns an error for empty input.
func parseCPUPct(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("parseCPUPct: empty string")
	}
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("parseCPUPct: %w", err)
	}
	return v, nil
}

// parseMemUsage parses a docker stats MemUsage string like "15.3MiB / 7.77GiB"
// and returns the used portion in kibibytes.
func parseMemUsage(s string) (int64, error) {
	parts := strings.SplitN(s, "/", 2)
	return parseDockerSize(strings.TrimSpace(parts[0]))
}

// parseDockerSize parses a Docker size string (e.g. "15.3MiB", "512kB", "1.2GB")
// and returns the value in kibibytes.
func parseDockerSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		bytes  float64
	}{
		{"GiB", 1024 * 1024 * 1024},
		{"MiB", 1024 * 1024},
		{"KiB", 1024},
		{"GB", 1e9},
		{"MB", 1e6},
		{"kB", 1e3},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			v, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("parseDockerSize: parse %q: %w", s, err)
			}
			return int64(v * u.bytes / 1024), nil
		}
	}
	return 0, fmt.Errorf("parseDockerSize: unrecognized unit in %q", s)
}

// parseVmRSS scans the content of a /proc/<pid>/status file and returns the
// VmRSS value in kibibytes. Kept for testing; production code uses docker stats.
func parseVmRSS(content string) (int64, error) {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			// fields: ["VmRSS:", "<value>", "kB"]
			if len(fields) >= 2 {
				return strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	return 0, fmt.Errorf("parseVmRSS: VmRSS line not found")
}

// RunPoller opens path for append (creating it if needed), then polls each
// container in containers every interval, writing one StatRecord per container
// per tick as a JSON line. It runs until ctx is cancelled.
//
// Per-container errors (e.g. container stopped mid-run) are logged and skipped;
// the poller continues for other containers.
func RunPoller(ctx context.Context, containers []string, path string, interval time.Duration) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("RunPoller: open %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case tick := <-ticker.C:
			ts := tick.UTC()
			records := make([]StatRecord, 0, len(containers))
			var mu sync.Mutex
			var wg sync.WaitGroup

			for _, name := range containers {
				name := name
				wg.Add(1)
				go func() {
					defer wg.Done()
					cs, err := readContainerStats(name)
					if err != nil {
						log.Printf("statsd: poll %s: %v", name, err)
						return
					}
					mu.Lock()
					records = append(records, StatRecord{
						Container: name,
						TS:        ts,
						CPUPCT:    cs.CPUPCT,
						VmRSSKB:   cs.VmRSSKB,
					})
					mu.Unlock()
				}()
			}
			wg.Wait()

			for _, rec := range records {
				if err := enc.Encode(rec); err != nil {
					log.Printf("statsd: encode record for %s: %v", rec.Container, err)
				}
			}
		}
	}
}
