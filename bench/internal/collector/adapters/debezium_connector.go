package adapters

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
	"github.com/twmb/franz-go/pkg/kgo"
)

// RunDebeziumConnector starts a Kafka consumer for the Debezium Kafka Connect topic
// and writes EventRecords to out. Debezium Connector streams WAL events in batches
// to Redpanda; events arrive in the standard Debezium envelope with after._bench_ts.
func RunDebeziumConnector(ctx context.Context, brokers []string, topic string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("bench-collector-debezium-connect"),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		log.Printf("debezium-connector adapter: create client: %v", err)
		return
	}
	defer cl.Close()

	for {
		fetches := cl.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		if ctx.Err() != nil {
			return
		}

		fetches.EachError(func(topic string, partition int32, err error) {
			log.Printf("debezium-connector adapter: fetch error topic=%s partition=%d: %v", topic, partition, err)
		})

		receiveTS := time.Now()

		sc := ""
		if s, ok := scenario.Load().(string); ok {
			sc = s
		}

		fetches.EachRecord(func(r *kgo.Record) {
			benchTS, ok := ExtractBenchTS(r.Value)
			if !ok {
				return
			}
			rec := collector.EventRecord{
				Tool:      "debezium-connector",
				Scenario:  sc,
				ReceiveTS: receiveTS,
				BenchTS:   benchTS,
				LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
			}
			select {
			case out <- rec:
			case <-ctx.Done():
				return
			}
		})
	}
}
