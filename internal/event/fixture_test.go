package event_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "regenerate golden fixture file")

const fixturesPath = "testdata/changeevent_fixtures.ndjson"

func fixtureEvents() []event.ChangeEvent {
	ts := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	return []event.ChangeEvent{
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A1"),
			IdempotencyKey: "pg:public.orders:1:insert:0/1A000001",
			Timestamp:      ts,
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpInsert,
			Database:       "shop",
			Schema:         "public",
			Table:          "orders",
			Key:            json.RawMessage(`{"id":1}`),
			Before:         nil,
			After:          json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"pending"}`),
			Metadata:       map[string]any{"lsn": "0/1A000001", "tx_id": float64(500)},
		},
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A2"),
			IdempotencyKey: "pg:public.orders:1:update:0/1A000002",
			Timestamp:      ts.Add(time.Second),
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpUpdate,
			Database:       "shop",
			Schema:         "public",
			Table:          "orders",
			Key:            json.RawMessage(`{"id":1}`),
			Before:         json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"pending"}`),
			After:          json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"shipped"}`),
			Metadata:       map[string]any{"lsn": "0/1A000002", "tx_id": float64(501)},
		},
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A3"),
			IdempotencyKey: "pg:public.orders:1:delete:0/1A000003",
			Timestamp:      ts.Add(2 * time.Second),
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpDelete,
			Database:       "shop",
			Schema:         "public",
			Table:          "orders",
			Key:            json.RawMessage(`{"id":1}`),
			Before:         json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"shipped"}`),
			After:          nil,
			Metadata:       map[string]any{"lsn": "0/1A000003", "tx_id": float64(502)},
		},
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A4"),
			IdempotencyKey: "pg:public.orders:2:read:snapshot/1",
			Timestamp:      ts.Add(3 * time.Second),
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpRead,
			Database:       "shop",
			Schema:         "public",
			Table:          "orders",
			Key:            json.RawMessage(`{"id":2}`),
			Before:         nil,
			After:          json.RawMessage(`{"id":2,"customer":"Bob","total":42.00,"status":"delivered"}`),
			Metadata:       map[string]any{"snapshot": true, "cursor": "id>1"},
		},
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A5"),
			IdempotencyKey: "pg:_kaptanto:heartbeat:control:0/1A000004",
			Timestamp:      ts.Add(4 * time.Second),
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpControl,
			Database:       "shop",
			Schema:         "_kaptanto",
			Table:          "_heartbeat",
			Key:            json.RawMessage(`{"type":"heartbeat"}`),
			Before:         nil,
			After:          json.RawMessage(`{"type":"heartbeat","node_id":"node-1"}`),
			Metadata:       map[string]any{"lsn": "0/1A000004", "signal": "heartbeat"},
		},
		{
			ID:             ulid.MustParse("01J5Z0000000000000000000A6"),
			IdempotencyKey: "pg:public.orders:1:update:0/1A000005",
			Timestamp:      ts.Add(5 * time.Second),
			Source:         "postgres://cdc@localhost:5432/shop",
			Operation:      event.OpUpdate,
			Database:       "shop",
			Schema:         "public",
			Table:          "orders",
			Key:            json.RawMessage(`{"id":1}`),
			Before:         json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"shipped"}`),
			After:          json.RawMessage(`{"id":1,"customer":"Alice","total":99.95,"status":"delivered"}`),
			Metadata:       map[string]any{"lsn": "0/1A000005", "tx_id": float64(503)},
			AIContext: json.RawMessage(
				`{"intent":"fulfill_order","entities":[{"type":"customer","value":"Alice","field":"customer"}],"suggested_actions":["notify_shipping"],"embedding":{"model":"text-embedding-3-small","vector":[0.1,0.2,0.3]},"custom":{"priority":"high"}}`,
			),
		},
	}
}

func generateFixtureBytes(t *testing.T) []byte {
	t.Helper()
	events := fixtureEvents()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, evt := range events {
		require.NoError(t, enc.Encode(evt))
	}
	return buf.Bytes()
}

func TestFixtures_Generate(t *testing.T) {
	generated := generateFixtureBytes(t)

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(fixturesPath), 0o755))
		require.NoError(t, os.WriteFile(fixturesPath, generated, 0o644))
		t.Log("fixtures regenerated: ", fixturesPath)
		return
	}

	committed, err := os.ReadFile(fixturesPath)
	require.NoError(t, err, "fixture file missing — run: go test ./internal/event -run TestFixtures_Generate -update")

	if !bytes.Equal(generated, committed) {
		t.Fatalf(
			"committed fixture file differs from generated output.\n"+
				"Run the following to regenerate:\n\n"+
				"  go test ./internal/event -run TestFixtures_Generate -update\n\n"+
				"Then review and commit the updated file.",
		)
	}
}

func TestFixtures_RoundTrip(t *testing.T) {
	data, err := os.ReadFile(fixturesPath)
	require.NoError(t, err, "fixture file missing — run: go test ./internal/event -run TestFixtures_Generate -update")

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	require.Len(t, lines, 6, "expected 6 fixture events (5 ops + 1 enriched)")

	expectedOps := []event.Operation{
		event.OpInsert,
		event.OpUpdate,
		event.OpDelete,
		event.OpRead,
		event.OpControl,
		event.OpUpdate, // enriched event
	}

	for i, line := range lines {
		var evt event.ChangeEvent
		require.NoError(t, json.Unmarshal(line, &evt), "line %d is not valid JSON", i+1)

		assert.Equal(t, expectedOps[i], evt.Operation, "line %d: unexpected operation", i+1)
		assert.NotEmpty(t, evt.ID.String(), "line %d: ID must be populated", i+1)
		assert.NotEmpty(t, evt.IdempotencyKey, "line %d: idempotency_key must be populated", i+1)
		assert.False(t, evt.Timestamp.IsZero(), "line %d: timestamp must be populated", i+1)
		assert.NotEmpty(t, evt.Source, "line %d: source must be populated", i+1)
		assert.NotEmpty(t, evt.Table, "line %d: table must be populated", i+1)
		assert.NotNil(t, evt.Key, "line %d: key must be populated", i+1)
		assert.NotNil(t, evt.Metadata, "line %d: metadata must be populated", i+1)

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &raw))
		if i < 5 {
			_, hasAI := raw["ai_context"]
			assert.False(t, hasAI, "line %d: ai_context must be omitted when empty", i+1)
			assert.Nil(t, evt.AIContext, "line %d: AIContext must be nil when omitted", i+1)
		} else {
			require.Contains(t, raw, "ai_context", "line %d: enriched fixture must include ai_context", i+1)
			require.NotNil(t, evt.AIContext, "line %d: AIContext must be populated", i+1)
			assert.JSONEq(t, string(raw["ai_context"]), string(evt.AIContext),
				"line %d: AIContext must round-trip unchanged", i+1)
		}

		// Re-marshal and compare to verify lossless round-trip.
		remarshaled, err := json.Marshal(evt)
		require.NoError(t, err, "line %d: re-marshal failed", i+1)

		var original, roundtripped map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &original))
		require.NoError(t, json.Unmarshal(remarshaled, &roundtripped))

		for key, origVal := range original {
			rtVal, ok := roundtripped[key]
			assert.True(t, ok, "line %d: round-trip missing key %q", i+1, key)
			assert.JSONEq(t, string(origVal), string(rtVal),
				"line %d: round-trip mismatch for key %q", i+1, key)
		}
		for key := range roundtripped {
			if _, ok := original[key]; !ok {
				t.Errorf("line %d: round-trip added extra key %q", i+1, key)
			}
		}
	}
}
