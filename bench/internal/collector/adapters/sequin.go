package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
)

type sequinEntry struct {
	AckID  string         `json:"ack_id"`
	Record map[string]any `json:"record"`
}

type sequinBody struct {
	Data []sequinEntry `json:"data"`
}

// sequinSingleRecord handles Sequin v0.14+ format: {"record": {...}, ...}
// where the record fields are delivered directly (not wrapped in a data array).
type sequinSingleRecord struct {
	Record map[string]any `json:"record"`
}

// SequinHandler returns an http.HandlerFunc for Sequin push webhook POSTs.
// Supports both the batched format {"data":[{"ack_id":"...","record":{...}}]}
// and the single-record format {"record":{...}} used by Sequin v0.14+.
func SequinHandler(scenario *atomic.Value, out chan<- collector.EventRecord) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)

		receiveTS := time.Now()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}

		sc := ""
		if s, ok2 := scenario.Load().(string); ok2 {
			sc = s
		}

		// Try batched format first: {"data": [{"ack_id": "...", "record": {...}}]}
		var payload sequinBody
		if err := json.Unmarshal(body, &payload); err == nil && len(payload.Data) > 0 {
			for _, entry := range payload.Data {
				if entry.Record == nil {
					continue
				}
				emitSequinRecord(entry.Record, sc, receiveTS, out)
			}
			return
		}

		// Fall back to single-record format: {"record": {...}, ...}
		var single sequinSingleRecord
		if err := json.Unmarshal(body, &single); err != nil {
			return
		}
		if single.Record != nil {
			emitSequinRecord(single.Record, sc, receiveTS, out)
			return
		}

		// Last resort: treat top-level object as the record itself
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err == nil {
			emitSequinRecord(raw, sc, receiveTS, out)
		}
	}
}

func emitSequinRecord(record map[string]any, sc string, receiveTS time.Time, out chan<- collector.EventRecord) {
	benchTSStr, ok := record["_bench_ts"].(string)
	if !ok || benchTSStr == "" {
		return
	}
	benchTS, err := parseBenchTS(benchTSStr)
	if err != nil {
		return
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
