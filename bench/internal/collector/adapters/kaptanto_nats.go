package adapters

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/olucasandrade/kaptanto/bench/internal/collector"
)

// RunKaptantoNATS subscribes to a core NATS subject written by the kaptanto NATS sink
// and forwards decoded EventRecords to out. It reconnects automatically on failure.
func RunKaptantoNATS(ctx context.Context, natsURL, subject string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	for {
		if ctx.Err() != nil {
			return
		}

		nc, err := nats.Connect(natsURL)
		if err != nil {
			log.Printf("kaptanto-nats adapter: connect %s: %v — retrying in 200ms", natsURL, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}

		sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			receiveTS := time.Now()
			benchTS, ok := ExtractBenchTS(msg.Data)
			if !ok {
				return
			}
			sc := ""
			if s, ok2 := scenario.Load().(string); ok2 {
				sc = s
			}
			rec := collector.EventRecord{
				Tool:      "kaptanto-nats",
				Scenario:  sc,
				ReceiveTS: receiveTS,
				BenchTS:   benchTS,
				LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
			}
			select {
			case out <- rec:
			default:
			}
		})
		if err != nil {
			log.Printf("kaptanto-nats adapter: subscribe %s: %v — retrying in 200ms", subject, err)
			nc.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}

		<-ctx.Done()
		_ = sub.Unsubscribe()
		nc.Close()
		return
	}
}
