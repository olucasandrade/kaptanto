package router

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildDLQEntry_AIContextRipple covers G3-19 #15 for DLQ payloads when
// Entry.Raw is empty and the ChangeEvent is marshaled: present ai_context is
// preserved, absent is omitted (omitempty).
func TestBuildDLQEntry_AIContextRipple(t *testing.T) {
	aiCtx := json.RawMessage(`{"intent":"refund","entities":[{"type":"order","value":"42"}]}`)
	id := ulid.MustNew(ulid.Timestamp(time.Unix(1_700_000_000, 0)), nil)

	t.Run("present", func(t *testing.T) {
		entry := buildDLQEntry(&RetryRecord{
			ConsumerID: "c",
			Attempts:   3,
			LastErr:    errors.New("permanent"),
			Entry: eventlog.LogEntry{
				Seq:         7,
				PartitionID: 1,
				Event: &event.ChangeEvent{
					ID: id, Schema: "public", Table: "orders",
					Operation: event.OpUpdate, Key: json.RawMessage(`{"id":42}`),
					After: json.RawMessage(`{"id":42}`), AIContext: aiCtx,
					IdempotencyKey: "pg:orders:42:update:0/7",
				},
			},
		})
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(entry.Payload, &m))
		require.Contains(t, m, "ai_context")
		assert.JSONEq(t, string(aiCtx), string(m["ai_context"]))
	})

	t.Run("absent_omitted", func(t *testing.T) {
		entry := buildDLQEntry(&RetryRecord{
			ConsumerID: "c",
			Attempts:   3,
			LastErr:    errors.New("permanent"),
			Entry: eventlog.LogEntry{
				Seq:         8,
				PartitionID: 1,
				Event: &event.ChangeEvent{
					ID: id, Schema: "public", Table: "orders",
					Operation: event.OpInsert, Key: json.RawMessage(`{"id":1}`),
					After: json.RawMessage(`{"id":1}`),
					IdempotencyKey: "pg:orders:1:insert:0/8",
				},
			},
		})
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(entry.Payload, &m))
		assert.NotContains(t, m, "ai_context")
	})
}
