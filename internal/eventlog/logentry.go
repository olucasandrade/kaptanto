package eventlog

import (
	"encoding/json"
	"fmt"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// MaterializeEvent unmarshals Event from Raw when Event is nil. No-op when Event
// is already set or Raw is empty. Safe to call multiple times.
func (e *LogEntry) MaterializeEvent() error {
	if e == nil {
		return fmt.Errorf("eventlog: nil LogEntry")
	}
	if e.Event != nil {
		return nil
	}
	if len(e.Raw) == 0 {
		return fmt.Errorf("eventlog: LogEntry has no Raw bytes")
	}
	var ev event.ChangeEvent
	if err := json.Unmarshal(e.Raw, &ev); err != nil {
		return fmt.Errorf("eventlog: unmarshal event: %w", err)
	}
	e.Event = &ev
	return nil
}

// GroupingKey returns the CDC primary-key bytes used for per-key ordering (RTR-04).
// Uses a partial JSON decode when Event is nil to avoid a full ChangeEvent unmarshal.
func (e *LogEntry) GroupingKey() (string, error) {
	if e == nil {
		return "", fmt.Errorf("eventlog: nil LogEntry")
	}
	if e.Event != nil {
		return string(e.Event.Key), nil
	}
	if len(e.Raw) == 0 {
		return "", fmt.Errorf("eventlog: LogEntry has no Raw bytes")
	}
	var partial struct {
		Key json.RawMessage `json:"key"`
	}
	if err := json.Unmarshal(e.Raw, &partial); err != nil {
		return "", fmt.Errorf("eventlog: unmarshal key: %w", err)
	}
	return string(partial.Key), nil
}
