package adapters

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
	"github.com/twmb/franz-go/pkg/kgo"
)

// ExtractBenchTS walks a Kafka message value (JSON) looking for "_bench_ts".
// It checks top-level, then "after._bench_ts", then "record._bench_ts".
// Exported for testing.
func ExtractBenchTS(value []byte) (time.Time, bool) {
	var m map[string]any
	if err := json.Unmarshal(value, &m); err != nil {
		return time.Time{}, false
	}

	// 1. Top-level _bench_ts.
	if ts, ok := extractTSFromMap(m, "_bench_ts"); ok {
		return ts, true
	}

	// 2. after._bench_ts.
	if after, ok := m["after"].(map[string]any); ok {
		if ts, ok := extractTSFromMap(after, "_bench_ts"); ok {
			return ts, true
		}
	}

	// 3. record._bench_ts.
	if record, ok := m["record"].(map[string]any); ok {
		if ts, ok := extractTSFromMap(record, "_bench_ts"); ok {
			return ts, true
		}
	}

	return time.Time{}, false
}

func extractTSFromMap(m map[string]any, key string) (time.Time, bool) {
	v, ok := m[key]
	if !ok {
		return time.Time{}, false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	ts, err := parseBenchTS(s)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// RunPeerDB starts a Kafka consumer for the PeerDB topic and writes EventRecords to out.
func RunPeerDB(ctx context.Context, brokers []string, topic string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("bench-collector"),
		kgo.ConsumeTopics(topic),
	)
	if err != nil {
		log.Printf("peerdb adapter: create client: %v", err)
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
			log.Printf("peerdb adapter: fetch error topic=%s partition=%d: %v", topic, partition, err)
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
				Tool:      "peerdb",
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
