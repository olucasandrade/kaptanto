package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kaptanto/kaptanto/bench/internal/collector"
	"github.com/kaptanto/kaptanto/bench/internal/collector/adapters"
)

func main() {
	output := flag.String("output", "metrics.jsonl", "Path for NDJSON output file")
	kaptantoURL := flag.String("kaptanto-url", "http://localhost:7654/stream?consumer=collector", "Kaptanto SSE URL")
	debeziumPort := flag.Int("debezium-port", 8081, "HTTP port for Debezium webhook receiver")
	sequinPort := flag.Int("sequin-port", 8082, "HTTP port for Sequin push receiver")
	kafkaBrokers := flag.String("kafka-brokers", "localhost:9092", "Comma-separated Redpanda/Kafka brokers")
	kafkaTopic := flag.String("kafka-topic", "public.bench_events", "PeerDB Kafka topic")
	managementPort := flag.Int("management-port", 8080, "HTTP port for scenario management API")
	flag.Parse()

	// Shared adapter channel (buffered, adapters never block HTTP handlers).
	adapterCh := make(chan collector.EventRecord, 10000)

	// Writer channel (fan-out goroutine forwards from adapterCh to this).
	records := make(chan collector.EventRecord, 10000)

	// Current scenario tag — adapters read atomically on each event.
	var scenario atomic.Value
	scenario.Store("unknown")

	// lastSeen tracks the most recent receive_ts per tool for the management API.
	var lastSeen struct {
		mu sync.Mutex
		m  map[string]time.Time
	}
	lastSeen.m = make(map[string]time.Time)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Fan-out goroutine: updates lastSeen map, then forwards to writer channel.
	go func() {
		for rec := range adapterCh {
			lastSeen.mu.Lock()
			lastSeen.m[rec.Tool] = rec.ReceiveTS
			lastSeen.mu.Unlock()
			select {
			case records <- rec:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Writer goroutine.
	go func() {
		if err := collector.RunWriter(ctx, *output, records); err != nil {
			log.Printf("collector: writer error: %v", err)
		}
	}()

	// Kaptanto SSE adapter goroutine.
	go adapters.RunKaptanto(ctx, *kaptantoURL, &scenario, adapterCh)

	// Debezium HTTP server.
	debeziumMux := http.NewServeMux()
	debeziumMux.HandleFunc("/ingest/debezium", adapters.DebeziumHandler(&scenario, adapterCh))
	debeziumSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *debeziumPort),
		Handler: debeziumMux,
	}
	go func() {
		if err := debeziumSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("collector: debezium server: %v", err)
		}
	}()

	// Sequin HTTP server.
	sequinMux := http.NewServeMux()
	sequinMux.HandleFunc("/ingest/sequin", adapters.SequinHandler(&scenario, adapterCh))
	sequinSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *sequinPort),
		Handler: sequinMux,
	}
	go func() {
		if err := sequinSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("collector: sequin server: %v", err)
		}
	}()

	// PeerDB Kafka adapter goroutine.
	brokerList := strings.Split(*kafkaBrokers, ",")
	go adapters.RunPeerDB(ctx, brokerList, *kafkaTopic, &scenario, adapterCh)

	// Management API server.
	mgmtMux := http.NewServeMux()

	// POST /scenario?name=X — sets scenario tag.
	// GET  /scenario       — returns current scenario name.
	mgmtMux.HandleFunc("/scenario", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			name := r.URL.Query().Get("name")
			if name == "" {
				http.Error(w, "missing ?name=", http.StatusBadRequest)
				return
			}
			scenario.Store(name)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			current := "unknown"
			if s, ok := scenario.Load().(string); ok {
				current = s
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, current)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /scenario/last-event?tool=X — returns last receive_ts for a tool.
	mgmtMux.HandleFunc("/scenario/last-event", func(w http.ResponseWriter, r *http.Request) {
		tool := r.URL.Query().Get("tool")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		lastSeen.mu.Lock()
		ts, found := lastSeen.m[tool]
		lastSeen.mu.Unlock()

		var tsStr string
		if found {
			tsStr = ts.UTC().Format(time.RFC3339Nano)
		}
		resp := map[string]string{
			"tool":            tool,
			"last_receive_ts": tsStr,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	mgmtSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *managementPort),
		Handler: mgmtMux,
	}
	go func() {
		if err := mgmtSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("collector: management server: %v", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "collector: ready (management=:%d, debezium=:%d, sequin=:%d)\n",
		*managementPort, *debeziumPort, *sequinPort)

	<-ctx.Done()
	log.Println("collector: shutting down")
}
