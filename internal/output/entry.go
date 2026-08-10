package output

import "github.com/olucasandrade/kaptanto/internal/eventlog"

// MaterializeEntry unmarshals entry.Event from entry.Raw when needed.
func MaterializeEntry(entry *eventlog.LogEntry) error {
	return entry.MaterializeEvent()
}
