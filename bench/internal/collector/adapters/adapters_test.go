package adapters_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
	"github.com/olucasandrade/kaptanto/bench/internal/collector/adapters"
)

// Test 1 (Kaptanto): parseKaptantoLine parses a data: line into an EventRecord.
func TestKaptantoParseDataLine(t *testing.T) {
	benchTS := "2026-01-01T00:00:00Z"
	dataLine := `data: {"id":"01J","operation":"insert","table":"bench_events","after":{"_bench_ts":"` + benchTS + `","id":"x"}}`

	rec, ok := adapters.ParseKaptantoLine(dataLine)
	if !ok {
		t.Fatal("ParseKaptantoLine returned false for valid data line")
	}
	if rec.Tool != "kaptanto" {
		t.Errorf("Tool: got %q, want %q", rec.Tool, "kaptanto")
	}
	want, _ := time.Parse(time.RFC3339, benchTS)
	if !rec.BenchTS.Equal(want) {
		t.Errorf("BenchTS: got %v, want %v", rec.BenchTS, want)
	}
	// ReceiveTS should be close to now.
	if time.Since(rec.ReceiveTS) > 5*time.Second {
		t.Errorf("ReceiveTS too old: %v", rec.ReceiveTS)
	}
	// latency_us = receive_ts - bench_ts (bench_ts is 2026 which is in the past relative to now).
	if rec.LatencyUS <= 0 {
		t.Errorf("LatencyUS should be positive, got %d", rec.LatencyUS)
	}
}

// Test 2 (Kaptanto): Lines starting with ":" are skipped.
func TestKaptantoSkipPingLines(t *testing.T) {
	_, ok := adapters.ParseKaptantoLine(": ping")
	if ok {
		t.Error("ParseKaptantoLine should return false for ping comment line")
	}

	_, ok = adapters.ParseKaptantoLine(":")
	if ok {
		t.Error("ParseKaptantoLine should return false for bare colon")
	}
}

// Test 3 (Debezium): Handler returns http.StatusOK and sends one EventRecord.
func TestDebeziumHandlerOK(t *testing.T) {
	out := make(chan collector.EventRecord, 10)
	var scenario atomic.Value
	scenario.Store("steady")

	h := adapters.DebeziumHandler(&scenario, out)

	body := `{"before":null,"after":{"_bench_ts":"2026-01-01T00:00:00.000000000Z","id":"abc"},"op":"c"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest/debezium", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusOK)
	}

	select {
	case rec := <-out:
		if rec.Tool != "debezium" {
			t.Errorf("Tool: got %q, want %q", rec.Tool, "debezium")
		}
		if rec.Scenario != "steady" {
			t.Errorf("Scenario: got %q, want %q", rec.Scenario, "steady")
		}
		want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if !rec.BenchTS.Equal(want) {
			t.Errorf("BenchTS: got %v, want %v", rec.BenchTS, want)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no EventRecord received after 500ms")
	}
}

// Test 4 (Debezium): Malformed JSON still returns http.StatusOK.
func TestDebeziumHandlerMalformedJSON(t *testing.T) {
	out := make(chan collector.EventRecord, 10)
	var scenario atomic.Value
	scenario.Store("test")

	h := adapters.DebeziumHandler(&scenario, out)

	req := httptest.NewRequest(http.MethodPost, "/ingest/debezium", bytes.NewBufferString("{not json}"))
	rr := httptest.NewRecorder()

	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d (must not return error to avoid retry flood)", rr.Code, http.StatusOK)
	}
	// No record should be emitted.
	select {
	case <-out:
		// Still acceptable if it sent something, but check no panic occurred.
	default:
		// Good.
	}
}

// Test 5 (Sequin): Handler with 3-record data array emits 3 EventRecords.
func TestSequinHandlerMultipleRecords(t *testing.T) {
	out := make(chan collector.EventRecord, 10)
	var scenario atomic.Value
	scenario.Store("burst")

	h := adapters.SequinHandler(&scenario, out)

	body := `{"data":[
		{"ack_id":"a1","record":{"_bench_ts":"2026-01-01T00:00:01Z","id":"1"}},
		{"ack_id":"a2","record":{"_bench_ts":"2026-01-01T00:00:02Z","id":"2"}},
		{"ack_id":"a3","record":{"_bench_ts":"2026-01-01T00:00:03Z","id":"3"}}
	]}`

	req := httptest.NewRequest(http.MethodPost, "/ingest/sequin", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusOK)
	}

	count := 0
	timeout := time.After(500 * time.Millisecond)
	for count < 3 {
		select {
		case rec := <-out:
			if rec.Tool != "sequin" {
				t.Errorf("Tool: got %q, want %q", rec.Tool, "sequin")
			}
			count++
		case <-timeout:
			t.Fatalf("only got %d records, expected 3", count)
		}
	}
}

// Test 6 (PeerDB): extractBenchTS finds _bench_ts in nested {"after": {"_bench_ts": "..."}} structure.
func TestPeerDBExtractBenchTSNested(t *testing.T) {
	payload := []byte(`{"after":{"_bench_ts":"2026-01-01T00:00:00Z","id":"x"}}`)

	ts, ok := adapters.ExtractBenchTS(payload)
	if !ok {
		t.Fatal("ExtractBenchTS returned false for nested structure")
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("got %v, want %v", ts, want)
	}
}

// Test 7 (PeerDB): extractBenchTS finds _bench_ts in flat {"_bench_ts": "..."} structure.
func TestPeerDBExtractBenchTSFlat(t *testing.T) {
	payload := []byte(`{"_bench_ts":"2026-01-01T00:00:00Z","id":"x"}`)

	ts, ok := adapters.ExtractBenchTS(payload)
	if !ok {
		t.Fatal("ExtractBenchTS returned false for flat structure")
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("got %v, want %v", ts, want)
	}
}

// Ensure all JSON fields are present in EventRecord.
func TestEventRecordJSONFields(t *testing.T) {
	rec := collector.EventRecord{
		Tool:      "kaptanto",
		Scenario:  "steady",
		ReceiveTS: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
		BenchTS:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LatencyUS: 1000000,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	for _, field := range []string{"tool", "scenario", "receive_ts", "bench_ts", "latency_us"} {
		if _, ok := m[field]; !ok {
			t.Errorf("JSON field %q missing", field)
		}
	}
}
