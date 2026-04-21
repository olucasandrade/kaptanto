package adapters

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/olucasandrade/kaptanto/bench/internal/collector"
)

// RunKaptantoRust connects to the Rust-FFI Kaptanto SSE endpoint.
// It reuses ParseKaptantoLine but tags events as "kaptanto-rust".
func RunKaptantoRust(ctx context.Context, url string, scenario *atomic.Value, out chan<- collector.EventRecord) {
	for {
		if ctx.Err() != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			log.Printf("kaptanto-rust adapter: create request: %v", err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("kaptanto-rust adapter: connect: %v — retrying in 200ms", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 128*1024), 128*1024)

		for scanner.Scan() {
			rec, ok := ParseKaptantoLine(scanner.Text())
			if !ok {
				continue
			}
			rec.Tool = "kaptanto-rust"
			if sc, ok2 := scenario.Load().(string); ok2 {
				rec.Scenario = sc
			}
			select {
			case out <- rec:
			case <-ctx.Done():
				resp.Body.Close()
				return
			}
		}

		resp.Body.Close()

		if ctx.Err() != nil {
			return
		}
		log.Printf("kaptanto-rust adapter: stream ended — retrying in 200ms")
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
