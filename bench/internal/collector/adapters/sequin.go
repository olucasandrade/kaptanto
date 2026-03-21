package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kaptanto/kaptanto/bench/internal/collector"
)

type sequinEntry struct {
	AckID  string         `json:"ack_id"`
	Record map[string]any `json:"record"`
}

type sequinBody struct {
	Data []sequinEntry `json:"data"`
}

// SequinHandler returns an http.HandlerFunc for Sequin push webhook POSTs.
// One EventRecord is emitted per valid entry in the data array.
// Exported as SequinHandler for main.go registration.
func SequinHandler(scenario *atomic.Value, out chan<- collector.EventRecord) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always 200 first.
		w.WriteHeader(http.StatusOK)

		receiveTS := time.Now()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}

		var payload sequinBody
		if err := json.Unmarshal(body, &payload); err != nil {
			return
		}

		sc := ""
		if s, ok2 := scenario.Load().(string); ok2 {
			sc = s
		}

		for _, entry := range payload.Data {
			if entry.Record == nil {
				continue
			}
			benchTSStr, ok := entry.Record["_bench_ts"].(string)
			if !ok || benchTSStr == "" {
				continue
			}
			benchTS, err := parseBenchTS(benchTSStr)
			if err != nil {
				continue
			}
			rec := collector.EventRecord{
				Tool:      "sequin",
				Scenario:  sc,
				ReceiveTS: receiveTS,
				BenchTS:   benchTS,
				LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
			}
			select {
			case out <- rec:
			default:
				// Channel full — drop.
			}
		}
	}
}
