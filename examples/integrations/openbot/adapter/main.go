package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	maxRequestBytes = 64 << 10
	eventType       = "kaptanto.agent-task.requested"
)

type changeEvent struct {
	ID             string          `json:"id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Timestamp      time.Time       `json:"timestamp"`
	Operation      string          `json:"operation"`
	Schema         string          `json:"schema"`
	Table          string          `json:"table"`
	After          json.RawMessage `json:"after"`
}

type agentTask struct {
	TaskType    string          `json:"task_type"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	Payload     json.RawMessage `json:"payload"`
	RequestedBy string          `json:"requested_by"`
	CreatedAt   time.Time       `json:"created_at"`
}

type openBotEvent struct {
	Type       string      `json:"type"`
	OccurredAt time.Time   `json:"occurredAt"`
	Data       openBotData `json:"data"`
}

type openBotData struct {
	EventID     string          `json:"eventId"`
	TaskType    string          `json:"taskType"`
	EntityType  string          `json:"entityType"`
	EntityID    string          `json:"entityId"`
	Payload     json.RawMessage `json:"payload"`
	RequestedBy string          `json:"requestedBy,omitempty"`
}

type handler struct {
	upstreamURL string
	secret      []byte
	relayToken  string
	allowed     map[string]struct{}
	client      *http.Client
	now         func() time.Time
}

func main() {
	if err := run(); err != nil {
		slog.Error("adapter stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	upstream, err := parseUpstream(os.Getenv("OPENBOT_WEBHOOK_URL"))
	if err != nil {
		return err
	}
	secret := os.Getenv("OPENBOT_WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("OPENBOT_WEBHOOK_SECRET is required")
	}
	relayToken := os.Getenv("OPENBOT_RELAY_TOKEN")
	if relayToken == "" {
		return errors.New("OPENBOT_RELAY_TOKEN is required")
	}
	allowed := parseAllowlist(os.Getenv("OPENBOT_ALLOWED_TASK_TYPES"))
	if len(allowed) == 0 {
		return errors.New("OPENBOT_ALLOWED_TASK_TYPES must name at least one task type")
	}
	addr := os.Getenv("OPENBOT_ADAPTER_ADDR")
	if addr == "" {
		addr = ":8787"
	}

	h := &handler{
		upstreamURL: upstream,
		secret:      []byte(secret),
		relayToken:  relayToken,
		allowed:     allowed,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
	slog.Info("OpenBot adapter listening", "addr", addr, "allowed_task_types", len(allowed))
	return http.ListenAndServe(addr, h.routes())
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/events", h)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func parseUpstream(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("OPENBOT_WEBHOOK_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", errors.New("OPENBOT_WEBHOOK_URL must be an absolute HTTP(S) URL without user info")
	}
	return raw, nil
}

func parseAllowlist(raw string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out[item] = struct{}{}
		}
	}
	return out
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !authorized(r.Header.Get("Authorization"), h.relayToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	event, task, err := decodeTask(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := h.allowed[task.TaskType]; !ok {
		http.Error(w, "task type is not allowlisted", http.StatusBadRequest)
		return
	}
	occurredAt := task.CreatedAt
	if occurredAt.IsZero() {
		occurredAt = event.Timestamp
	}
	if occurredAt.IsZero() {
		occurredAt = h.now()
	}
	body, err := json.Marshal(openBotEvent{
		Type:       eventType,
		OccurredAt: occurredAt.UTC(),
		Data: openBotData{
			EventID:     event.ID,
			TaskType:    task.TaskType,
			EntityType:  task.EntityType,
			EntityID:    task.EntityID,
			Payload:     task.Payload,
			RequestedBy: task.RequestedBy,
		},
	})
	if err != nil || len(body) > maxRequestBytes {
		http.Error(w, "cannot encode a valid OpenBot event", http.StatusBadRequest)
		return
	}
	status, err := h.forward(r.Context(), body, event.IdempotencyKey)
	if err != nil {
		slog.Warn("OpenBot delivery failed", "event_id", event.ID, "task_type", task.TaskType, "error", err)
		http.Error(w, "upstream delivery failed", http.StatusBadGateway)
		return
	}
	slog.Info("OpenBot delivery completed", "event_id", event.ID, "task_type", task.TaskType, "status", status)
	w.WriteHeader(status)
}

func authorized(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func decodeTask(r io.Reader) (changeEvent, agentTask, error) {
	var event changeEvent
	if err := decodeOne(r, &event); err != nil {
		return event, agentTask{}, fmt.Errorf("invalid Kaptanto event: %w", err)
	}
	if event.ID == "" || event.IdempotencyKey == "" {
		return event, agentTask{}, errors.New("event id and idempotency key are required")
	}
	if event.Operation != "insert" || event.Schema != "public" || event.Table != "agent_task_requests" {
		return event, agentTask{}, errors.New("only public.agent_task_requests inserts are accepted")
	}
	var task agentTask
	if len(event.After) == 0 || bytes.Equal(event.After, []byte("null")) {
		return event, agentTask{}, errors.New("insert event has no after row")
	}
	if err := decodeOne(bytes.NewReader(event.After), &task); err != nil {
		return event, agentTask{}, fmt.Errorf("invalid agent task: %w", err)
	}
	if task.TaskType == "" || task.EntityType == "" || task.EntityID == "" {
		return event, agentTask{}, errors.New("task type, entity type, and entity id are required")
	}
	if len(task.Payload) == 0 {
		task.Payload = json.RawMessage(`{}`)
	}
	var payload map[string]json.RawMessage
	if err := decodeOne(bytes.NewReader(task.Payload), &payload); err != nil || payload == nil {
		return event, agentTask{}, errors.New("task payload must be a JSON object")
	}
	return event, task, nil
}

func decodeOne(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

func (h *handler) forward(ctx context.Context, body []byte, idempotencyKey string) (int, error) {
	deliverySum := sha256.Sum256([]byte(idempotencyKey))
	deliveryID := hex.EncodeToString(deliverySum[:])
	timestamp := fmt.Sprintf("%d", h.now().Unix())
	mac := hmac.New(sha256.New, h.secret)
	_, _ = io.WriteString(mac, timestamp)
	_, _ = io.WriteString(mac, ".")
	_, _ = io.WriteString(mac, deliveryID)
	_, _ = io.WriteString(mac, ".")
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenBot-Timestamp", timestamp)
	req.Header.Set("X-OpenBot-Delivery-Id", deliveryID)
	req.Header.Set("X-OpenBot-Signature", "sha256="+signature)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}
