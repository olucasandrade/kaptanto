package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
)

type debeziumBody struct {
	Before map[string]any `json:"before"`
	After  map[string]any `json:"after"`
	Op     string         `json:"op"`
}

// DebeziumHandler returns an http.HandlerFunc that accepts Debezium HTTP sink POSTs.
// CRITICAL: writes 200 before processing to prevent Debezium retry floods.
// Exported as DebeziumHandler so main.go can register it.
func DebeziumHandler(scenario *atomic.Value, out chan<- collector.EventRecord) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always respond 200 immediately — Debezium treats non-2xx as retriable.
		w.WriteHeader(http.StatusOK)

		receiveTS := time.Now()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}

		var ev debeziumBody
		if err := json.Unmarshal(body, &ev); err != nil {
			return
		}

		if ev.After == nil {
			return
		}

		benchTSStr, ok := ev.After["_bench_ts"].(string)
		if !ok || benchTSStr == "" {
			return
		}

		benchTS, err := parseBenchTS(benchTSStr)
		if err != nil {
			return
		}

		sc := ""
		if s, ok2 := scenario.Load().(string); ok2 {
			sc = s
		}

		rec := collector.EventRecord{
			Tool:      "debezium",
			Scenario:  sc,
			ReceiveTS: receiveTS,
			BenchTS:   benchTS,
			LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
		}

		select {
		case out <- rec:
		default:
			// Channel full — drop rather than block the HTTP handler.
		}
	}
}

// Handler is the package-level export matching the plan's artifact spec.
func Handler(scenario *atomic.Value, out chan<- collector.EventRecord) http.HandlerFunc {
	return DebeziumHandler(scenario, out)
}
