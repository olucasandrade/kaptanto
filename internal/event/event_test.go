package event_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangeEvent_InsertSerializesToJSON(t *testing.T) {
	evt := event.ChangeEvent{
		IdempotencyKey: "pg:public.users:1:insert:0/1A2B3C4",
		Timestamp:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:         "postgres://localhost/mydb",
		Operation:      event.OpInsert,
		Table:          "users",
		Key:            json.RawMessage(`{"id":1}`),
		Before:         nil,
		After:          json.RawMessage(`{"id":1,"name":"Alice"}`),
		Metadata:       map[string]any{"lsn": "0/1A2B3C4"},
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))

	// All required top-level keys must be present.
	requiredKeys := []string{"id", "idempotency_key", "timestamp", "source", "operation", "table", "key", "before", "after", "metadata"}
	for _, k := range requiredKeys {
		assert.Contains(t, m, k, "missing required key: %s", k)
	}

	// Operation must be "insert".
	var op string
	require.NoError(t, json.Unmarshal(m["operation"], &op))
	assert.Equal(t, "insert", op)

	// before must be null for inserts.
	assert.Equal(t, "null", string(m["before"]))

	// after must not be null for inserts.
	assert.NotEqual(t, "null", string(m["after"]))
}

func TestChangeEvent_DeleteSerializesToJSON(t *testing.T) {
	evt := event.ChangeEvent{
		IdempotencyKey: "pg:public.users:1:delete:0/2B3C4D5",
		Timestamp:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:         "postgres://localhost/mydb",
		Operation:      event.OpDelete,
		Table:          "users",
		Key:            json.RawMessage(`{"id":1}`),
		Before:         json.RawMessage(`{"id":1,"name":"Alice"}`),
		After:          nil,
		Metadata:       map[string]any{"lsn": "0/2B3C4D5"},
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))

	// after must be null for deletes.
	assert.Equal(t, "null", string(m["after"]))

	// before must not be null for deletes.
	assert.NotEqual(t, "null", string(m["before"]))

	var op string
	require.NoError(t, json.Unmarshal(m["operation"], &op))
	assert.Equal(t, "delete", op)
}

func TestChangeEvent_UpdateSerializesToJSON(t *testing.T) {
	evt := event.ChangeEvent{
		IdempotencyKey: "pg:public.users:1:update:0/3C4D5E6",
		Timestamp:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:         "postgres://localhost/mydb",
		Operation:      event.OpUpdate,
		Table:          "users",
		Key:            json.RawMessage(`{"id":1}`),
		Before:         json.RawMessage(`{"id":1,"name":"Alice"}`),
		After:          json.RawMessage(`{"id":1,"name":"Bob"}`),
		Metadata:       map[string]any{"lsn": "0/3C4D5E6"},
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))

	// Both before and after must be present for updates.
	assert.NotEqual(t, "null", string(m["before"]))
	assert.NotEqual(t, "null", string(m["after"]))

	var op string
	require.NoError(t, json.Unmarshal(m["operation"], &op))
	assert.Equal(t, "update", op)
}

func TestChangeEvent_JSONFieldNames(t *testing.T) {
	evt := event.ChangeEvent{
		IdempotencyKey: "pg:public.orders:42:insert:0/1",
		Timestamp:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:         "pg",
		Operation:      event.OpInsert,
		Table:          "orders",
		Key:            json.RawMessage(`{"id":42}`),
		Before:         nil,
		After:          json.RawMessage(`{"id":42}`),
		Metadata:       map[string]any{},
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))

	expectedFields := []string{
		"id",
		"idempotency_key",
		"timestamp",
		"source",
		"operation",
		"table",
		"key",
		"before",
		"after",
		"metadata",
	}
	for _, f := range expectedFields {
		assert.Contains(t, m, f, "JSON output missing field: %s", f)
	}
}
