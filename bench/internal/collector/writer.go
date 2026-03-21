package collector

import (
	"context"
	"encoding/json"
	"os"
	"time"
)

// EventRecord is written as one NDJSON line per CDC event received.
type EventRecord struct {
	Tool      string    `json:"tool"`
	Scenario  string    `json:"scenario"`
	ReceiveTS time.Time `json:"receive_ts"`
	BenchTS   time.Time `json:"bench_ts"`
	LatencyUS int64     `json:"latency_us"`
}

// RunWriter opens (or creates/appends) the NDJSON file at path and writes one
// JSON line per EventRecord received on records. It is the only goroutine that
// writes to the file — no mutex needed. RunWriter returns nil when ctx is
// cancelled or the records channel is closed.
func RunWriter(ctx context.Context, path string, records <-chan EventRecord) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	for {
		select {
		case <-ctx.Done():
			return nil
		case rec, ok := <-records:
			if !ok {
				return nil
			}
			// Encode never returns an error for a well-formed struct; ignore it
			// to keep the hot path allocation-free.
			_ = enc.Encode(rec)
		}
	}
}
