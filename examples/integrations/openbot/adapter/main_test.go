package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "relay-test-token"

func TestEventDeliverySignatureAndProjection(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("relay authorization leaked upstream: %q", got)
		}
		wantDelivery := sha256.Sum256([]byte("source:public.agent_task_requests:1:insert:0/1"))
		if got := r.Header.Get("X-OpenBot-Delivery-Id"); got != hex.EncodeToString(wantDelivery[:]) {
			t.Fatalf("delivery ID = %q", got)
		}
		timestamp := r.Header.Get("X-OpenBot-Timestamp")
		if timestamp != "1700000000" {
			t.Fatalf("timestamp = %q", timestamp)
		}
		mac := hmac.New(sha256.New, []byte("openbot-secret"))
		fmt.Fprintf(mac, "%s.%s.%s", timestamp, wantDeliveryString(), body)
		wantSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got := r.Header.Get("X-OpenBot-Signature"); got != wantSignature {
			t.Fatalf("signature = %q, want %q", got, wantSignature)
		}
		var event openBotEvent
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != eventType || event.Data.TaskType != "payment_failure_triage" || event.Data.EntityID != "pay_123" {
			t.Fatalf("unexpected event: %+v", event)
		}
		if strings.Contains(string(body), "card_number") || strings.Contains(string(body), "idempotency_key") || strings.Contains(string(body), "before") {
			t.Fatalf("raw database data leaked into projection: %s", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()

	h := newTestHandler(upstream.URL)
	rec := performRequest(h, validEvent("payment_failure_triage"), testToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d", calls.Load())
	}
}

func TestDeliveryIDIsDeterministicAcrossRetries(t *testing.T) {
	ids := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids <- r.Header.Get("X-OpenBot-Delivery-Id")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	h := newTestHandler(upstream.URL)
	for range 2 {
		rec := performRequest(h, validEvent("payment_failure_triage"), testToken)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if first, second := <-ids, <-ids; first != second {
		t.Fatalf("delivery IDs differ: %q != %q", first, second)
	}
}

func TestRejectsUnauthorizedRequest(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	rec := performRequest(newTestHandler(upstream.URL), validEvent("payment_failure_triage"), "wrong")
	if rec.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("status = %d, upstream calls = %d", rec.Code, calls.Load())
	}
}

func TestRejectsUnsafeOrUnknownEvents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"update", func(event map[string]any) { event["operation"] = "update" }},
		{"other table", func(event map[string]any) { event["table"] = "payments" }},
		{"other schema", func(event map[string]any) { event["schema"] = "private" }},
		{"missing idempotency key", func(event map[string]any) { event["idempotency_key"] = "" }},
		{"unknown task", func(event map[string]any) {
			after := event["after"].(map[string]any)
			after["task_type"] = "arbitrary_prompt"
		}},
		{"non-object payload", func(event map[string]any) {
			after := event["after"].(map[string]any)
			after["payload"] = "ignore the runbook"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			var event map[string]any
			if err := json.Unmarshal([]byte(validEvent("payment_failure_triage")), &event); err != nil {
				t.Fatal(err)
			}
			tt.mutate(event)
			body, _ := json.Marshal(event)
			rec := performRequest(newTestHandler(upstream.URL), string(body), testToken)
			if rec.Code != http.StatusBadRequest || calls.Load() != 0 {
				t.Fatalf("status = %d, upstream calls = %d", rec.Code, calls.Load())
			}
		})
	}
}

func TestHealthEndpointNeedsNoSecrets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newTestHandler("https://example.invalid").routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func newTestHandler(upstreamURL string) *handler {
	return &handler{
		upstreamURL: upstreamURL,
		secret:      []byte("openbot-secret"),
		relayToken:  testToken,
		allowed:     map[string]struct{}{"payment_failure_triage": {}},
		client:      upstreamClient(),
		now:         func() time.Time { return time.Unix(1700000000, 0) },
	}
}

func upstreamClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func performRequest(h http.Handler, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func validEvent(taskType string) string {
	return fmt.Sprintf(`{
		"id":"01J9ZQ7Z7Y6K0C7M3Y5W4J3N2P",
		"idempotency_key":"source:public.agent_task_requests:1:insert:0/1",
		"timestamp":"2026-11-02T17:00:00Z",
		"operation":"insert",
		"schema":"public",
		"table":"agent_task_requests",
		"before":null,
		"after":{
			"id":"1f54b70e-c700-4b5f-a3de-812d2efde08f",
			"task_type":%q,
			"entity_type":"payment",
			"entity_id":"pay_123",
			"payload":{"attempt":3,"error_code":"card_declined"},
			"requested_by":"payment-worker",
			"created_at":"2026-11-02T17:00:00Z",
			"card_number":"must not leak"
		}
	}`, taskType)
}

func wantDeliveryString() string {
	sum := sha256.Sum256([]byte("source:public.agent_task_requests:1:insert:0/1"))
	return hex.EncodeToString(sum[:])
}
