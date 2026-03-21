package collector_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/bench/internal/collector"
)

// Test 1: RunWriter writes exactly one JSON line per EventRecord sent on the channel.
func TestWriterOneRecordPerLine(t *testing.T) {
	f, err := os.CreateTemp("", "writer_test_*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	records := make(chan collector.EventRecord, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- collector.RunWriter(ctx, f.Name(), records)
	}()

	benchTS := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	receiveTS := benchTS.Add(500 * time.Microsecond)

	records <- collector.EventRecord{
		Tool:      "kaptanto",
		Scenario:  "steady",
		ReceiveTS: receiveTS,
		BenchTS:   benchTS,
		LatencyUS: 500,
	}

	// Give writer time to flush.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := countNonEmptyLines(data)
	if lines != 1 {
		t.Errorf("expected 1 line, got %d; content: %q", lines, string(data))
	}
}

// Test 2: Two goroutines sending 500 records each produce exactly 1000 valid JSON lines with no interleaving.
func TestWriterConcurrentNoInterleave(t *testing.T) {
	f, err := os.CreateTemp("", "writer_concurrent_*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	records := make(chan collector.EventRecord, 10000)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- collector.RunWriter(ctx, f.Name(), records)
	}()

	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(tool string) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				ts := time.Now()
				records <- collector.EventRecord{
					Tool:      tool,
					Scenario:  "test",
					ReceiveTS: ts,
					BenchTS:   ts,
					LatencyUS: 0,
				}
			}
		}([]string{"tool-a", "tool-b"}[g])
	}
	wg.Wait()

	// Give writer time to flush all records.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	scanner := bufio.NewScanner(f)
	// Re-open to scan.
	ff, _ := os.Open(f.Name())
	defer ff.Close()
	scanner = bufio.NewScanner(ff)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var rec collector.EventRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("line %d: invalid JSON: %q, err: %v", count+1, line, err)
		}
		count++
	}

	if count != 1000 {
		t.Errorf("expected 1000 lines, got %d; content length=%d", count, len(data))
	}
}

// Test 3: RunWriter returns when the context is cancelled (no goroutine leak).
func TestWriterContextCancel(t *testing.T) {
	f, err := os.CreateTemp("", "writer_cancel_*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	records := make(chan collector.EventRecord, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- collector.RunWriter(ctx, f.Name(), records)
	}()

	select {
	case <-done:
		// Good — returned after context cancellation.
	case <-time.After(1 * time.Second):
		t.Fatal("RunWriter did not return after context cancellation")
	}
}

// Test 4: Each written line unmarshals back to EventRecord with all fields intact.
func TestWriterFieldsIntact(t *testing.T) {
	f, err := os.CreateTemp("", "writer_fields_*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	records := make(chan collector.EventRecord, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- collector.RunWriter(ctx, f.Name(), records)
	}()

	benchTS := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	receiveTS := benchTS.Add(1234 * time.Microsecond)

	original := collector.EventRecord{
		Tool:      "debezium",
		Scenario:  "burst",
		ReceiveTS: receiveTS,
		BenchTS:   benchTS,
		LatencyUS: 1234,
	}
	records <- original

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got collector.EventRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v, content: %q", err, string(data))
	}

	if got.Tool != original.Tool {
		t.Errorf("Tool: got %q, want %q", got.Tool, original.Tool)
	}
	if got.Scenario != original.Scenario {
		t.Errorf("Scenario: got %q, want %q", got.Scenario, original.Scenario)
	}
	if got.LatencyUS != original.LatencyUS {
		t.Errorf("LatencyUS: got %d, want %d", got.LatencyUS, original.LatencyUS)
	}
	if !got.ReceiveTS.Equal(original.ReceiveTS) {
		t.Errorf("ReceiveTS: got %v, want %v", got.ReceiveTS, original.ReceiveTS)
	}
	if !got.BenchTS.Equal(original.BenchTS) {
		t.Errorf("BenchTS: got %v, want %v", got.BenchTS, original.BenchTS)
	}
}

// Test 5: LatencyUS equals receive_ts minus bench_ts in microseconds.
func TestWriterLatencyCalculation(t *testing.T) {
	benchTS := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	receiveTS := benchTS.Add(7500 * time.Microsecond)
	expectedLatency := int64(7500)

	rec := collector.EventRecord{
		Tool:      "kaptanto",
		Scenario:  "steady",
		ReceiveTS: receiveTS,
		BenchTS:   benchTS,
		LatencyUS: receiveTS.Sub(benchTS).Microseconds(),
	}

	if rec.LatencyUS != expectedLatency {
		t.Errorf("LatencyUS: got %d, want %d", rec.LatencyUS, expectedLatency)
	}

	// Verify round-trip through JSON preserves it.
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got collector.EventRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.LatencyUS != expectedLatency {
		t.Errorf("round-trip LatencyUS: got %d, want %d", got.LatencyUS, expectedLatency)
	}
}

func countNonEmptyLines(data []byte) int {
	n := 0
	scanner := bufio.NewScanner(bufio.NewReader(
		&bytesReader{b: data, pos: 0},
	))
	for scanner.Scan() {
		if scanner.Text() != "" {
			n++
		}
	}
	return n
}

type bytesReader struct {
	b   []byte
	pos int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, os.ErrClosed
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
