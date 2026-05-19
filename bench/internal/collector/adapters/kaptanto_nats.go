package adapters

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/olucasandrade/kaptanto/bench/internal/collector"
)

// RunKaptantoNATS subscribes to the kaptanto NATS JetStream stream and forwards
// decoded EventRecords to out. Uses a durable consumer with DeliverAll policy so
// events published before the collector starts are not lost.
func RunKaptantoNATS(ctx context.Context, natsURL, subject string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	for {
		if ctx.Err() != nil {
			return
		}

		nc, err := nats.Connect(natsURL)
		if err != nil {
			log.Printf("kaptanto-nats adapter: connect %s: %v — retrying in 500ms", natsURL, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		js, err := jetstream.New(nc)
		if err != nil {
			log.Printf("kaptanto-nats adapter: jetstream init: %v — retrying in 500ms", err)
			nc.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		// Durable consumer with DeliverAll so all events from offset 0 are received.
		cons, err := js.CreateOrUpdateConsumer(ctx, "bench_cdc", jetstream.ConsumerConfig{
			Durable:       "bench-collector-nats",
			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,
			FilterSubject: subject,
		})
		if err != nil {
			log.Printf("kaptanto-nats adapter: create consumer: %v — retrying in 500ms", err)
			nc.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		msgs, err := cons.Messages()
		if err != nil {
			log.Printf("kaptanto-nats adapter: messages: %v — retrying in 500ms", err)
			nc.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		for {
			msg, err := msgs.Next()
			if err != nil {
				if ctx.Err() != nil {
					msgs.Stop()
					nc.Close()
					return
				}
				log.Printf("kaptanto-nats adapter: next: %v — reconnecting", err)
				msgs.Stop()
				nc.Close()
				break
			}
			_ = msg.Ack()

			receiveTS := time.Now()
			benchTS, ok := ExtractBenchTS(msg.Data())
			if !ok {
				continue
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
			case <-ctx.Done():
				msgs.Stop()
				nc.Close()
				return
			}
		}
	}
}
