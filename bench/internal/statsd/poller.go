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

// parseVmRSS scans the content of a /proc/<pid>/status file and returns the
// VmRSS value in kibibytes. Returns an error if the VmRSS line is absent.
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

// readVmRSSFromHost uses docker inspect to obtain the container's host PID,
// then reads /proc/<pid>/status directly from the host proc filesystem.
// The statsd container must run with pid: "host" for this to work.
func readVmRSSFromHost(containerName string) (int64, error) {
	pidOut, err := exec.Command("docker", "inspect",
		"--format", "{{.State.Pid}}", containerName).Output()
	if err != nil {
		return 0, fmt.Errorf("readVmRSSFromHost: docker inspect %s: %w", containerName, err)
	}
	pid := strings.TrimSpace(string(pidOut))
	if pid == "" || pid == "0" {
		return 0, fmt.Errorf("readVmRSSFromHost: container %s has no PID (stopped?)", containerName)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%s/status", pid))
	if err != nil {
		return 0, fmt.Errorf("readVmRSSFromHost: read /proc/%s/status: %w", pid, err)
	}
	return parseVmRSS(string(data))
}

// readCPUPct calls docker stats --no-stream for a single container and returns
// the CPU usage percentage.
func readCPUPct(containerName string) (float64, error) {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}", containerName).Output()
	if err != nil {
		return 0, fmt.Errorf("readCPUPct: docker stats %s: %w", containerName, err)
	}
	var row struct {
		CPUPerc string `json:"CPUPerc"`
	}
	if err := json.Unmarshal(out, &row); err != nil {
		return 0, fmt.Errorf("readCPUPct: unmarshal: %w (output: %q)", err, out)
	}
	return parseCPUPct(row.CPUPerc)
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
					cpu, cpuErr := readCPUPct(name)
					rss, rssErr := readVmRSSFromHost(name)
					if cpuErr != nil {
						log.Printf("statsd: cpu poll %s: %v", name, cpuErr)
					}
					if rssErr != nil {
						log.Printf("statsd: rss poll %s: %v", name, rssErr)
					}
					if cpuErr != nil && rssErr != nil {
						// Both failed — container likely stopped; skip record.
						return
					}
					mu.Lock()
					records = append(records, StatRecord{
						Container: name,
						TS:        ts,
						CPUPCT:    cpu,
						VmRSSKB:   rss,
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
