package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaptanto/kaptanto/bench/internal/collector"
)

type kaptantoEvent struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	Table     string          `json:"table"`
	After     json.RawMessage `json:"after"`
}

// ParseKaptantoLine parses a single SSE line from the Kaptanto stream.
// It returns a populated EventRecord and true if the line is a valid data: line.
// Lines starting with ":" (ping comments) return false.
// Exported for testing.
func ParseKaptantoLine(line string) (collector.EventRecord, bool) {
	if strings.HasPrefix(line, ":") {
		return collector.EventRecord{}, false
	}
	if !strings.HasPrefix(line, "data: ") {
		return collector.EventRecord{}, false
	}

	receiveTS := time.Now()
	payload := strings.TrimPrefix(line, "data: ")

	var evt kaptantoEvent
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return collector.EventRecord{}, false
	}

	// Extract after._bench_ts.
	var after map[string]any
	if err := json.Unmarshal(evt.After, &after); err != nil {
		return collector.EventRecord{}, false
	}

	benchTSStr, ok := after["_bench_ts"].(string)
	if !ok || benchTSStr == "" {
		return collector.EventRecord{}, false
	}

	benchTS, err := parseBenchTS(benchTSStr)
	if err != nil {
		return collector.EventRecord{}, false
	}

	return collector.EventRecord{
		Tool:      "kaptanto",
		ReceiveTS: receiveTS,
		BenchTS:   benchTS,
		LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
	}, true
}

// RunKaptanto connects to the Kaptanto SSE endpoint and streams events to out.
// It reads scenario from scenario.Load().(string) on each event.
// Also exported as Run for compatibility with plan artifact spec.
func RunKaptanto(ctx context.Context, url string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	for {
		if ctx.Err() != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			log.Printf("kaptanto adapter: create request: %v", err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("kaptanto adapter: connect: %v — retrying in 2s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 128*1024), 128*1024)

		for scanner.Scan() {
			line := scanner.Text()
			rec, ok := ParseKaptantoLine(line)
			if !ok {
				continue
			}
			if sc, ok2 := scenario.Load().(string); ok2 {
				rec.Scenario = sc
			}
			select {
			case out <- rec:
			case <-ctx.Done():
				resp.Body.Close()
				return
			}
		}

		resp.Body.Close()

		if ctx.Err() != nil {
			return
		}
		log.Printf("kaptanto adapter: stream ended — retrying in 2s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func parseBenchTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}
