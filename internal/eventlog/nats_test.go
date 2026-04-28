// Package eventlog_test provides black-box TDD tests for the NatsEventLog implementation.
// All tests use an in-process single-node NATS server for isolation and speed.
package eventlog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

// startTestNATS starts an in-process single-node NATS server with JetStream enabled.
// It registers server shutdown and connection close as t.Cleanup callbacks.
// Returns an open *nats.Conn connected to the test server.
func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL(), nats.Name("test"))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// openTestNatsEventLog opens a NatsEventLog using OpenNats with an embedded
// single-node NATS server (no peers). SyncAlways is false for unit tests —
// no OS crash risk in a test. R=1 because no peers → single-node stream.
func openTestNatsEventLog(t *testing.T) *eventlog.NatsEventLog {
	t.Helper()
	el, err := eventlog.OpenNats(eventlog.NatsEventLogConfig{
		Server: eventlog.NatsServerConfig{
			ClientPort: -1,
			StoreDir:   t.TempDir(),
			SyncAlways: false,
		},
		NumPartitions: 64,
		Retention:     time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = el.Close() })
	return el
}

// makeNatsEvent creates a ChangeEvent with the given idempotency key and key JSON,
// mirroring the makeEvent helper used in the Badger tests.
func makeNatsEvent(idempotencyKey string, keyJSON string) *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             ulid.Make(),
		IdempotencyKey: idempotencyKey,
		Timestamp:      time.Now(),
		Source:         "test-source",
		Operation:      event.OpInsert,
		Table:          "test_table",
		Key:            json.RawMessage(keyJSON),
		Before:         nil,
		After:          json.RawMessage(`{"col": "val"}`),
		Metadata:       map[string]any{"lsn": "0/1A2B3C4"},
	}
}

// TestNatsEventLogAppend verifies:
//   - Append returns seq >= 1 for a new event.
//   - Append returns seq=0 (duplicate sentinel) for the same IdempotencyKey appended a second time.
func TestNatsEventLogAppend(t *testing.T) {
	el := openTestNatsEventLog(t)

	ev := makeNatsEvent("nats:public.t:1:insert:0/1", `{"id": 1}`)

	seq1, err := el.Append(ev)
	require.NoError(t, err)
	require.Greater(t, seq1, uint64(0), "first Append must return seq >= 1")

	// Second Append with same IdempotencyKey must return seq=0 (duplicate sentinel, LOG-03).
	seq2, err := el.Append(ev)
	require.NoError(t, err)
	require.Equal(t, uint64(0), seq2, "duplicate Append must return seq=0")
}

// TestNatsEventLogReadPartition verifies:
//   - ReadPartition(ctx, partition, 1, 10) returns events written to that partition in order.
//   - Seq in LogEntry matches the stream sequence returned by Append.
func TestNatsEventLogReadPartition(t *testing.T) {
	el := openTestNatsEventLog(t)

	// Write several events that should land on one partition (deterministic key).
	// Key `{"id": 1}` hashes to a fixed partition via FNV-1a — use PartitionOf to
	// identify the exact partition rather than scanning all 64 (which would take
	// up to 64 × FetchMaxWait = ~128s and exceed the test timeout).
	key := `{"id": 1}`
	partition := eventlog.PartitionOf(json.RawMessage(key), 64)

	var written []struct {
		ev  *event.ChangeEvent
		seq uint64
	}
	for i := 1; i <= 3; i++ {
		ev := makeNatsEvent(fmt.Sprintf("nats:public.t:1:insert:0/%d", i), key)
		seq, err := el.Append(ev)
		require.NoError(t, err)
		require.Greater(t, seq, uint64(0))
		written = append(written, struct {
			ev  *event.ChangeEvent
			seq uint64
		}{ev, seq})
	}

	// Read only the correct partition — no scanning needed.
	ctx := context.Background()
	entries, err := el.ReadPartition(ctx, partition, 1, 10)
	require.NoError(t, err)
	require.Len(t, entries, len(written),
		"ReadPartition must return all written events for the partition")

	for i, entry := range entries {
		require.Equal(t, written[i].seq, entry.Seq,
			"LogEntry.Seq must match the seq returned by Append (in order)")
		require.Equal(t, written[i].ev.IdempotencyKey, entry.Event.IdempotencyKey,
			"events must be returned in write order")
	}
}

// TestNatsEventLogAppendBatch verifies:
//   - AppendBatch of N events returns a slice of length N.
//   - Duplicate events return seq=0; non-duplicate events return seq >= 1.
func TestNatsEventLogAppendBatch(t *testing.T) {
	el := openTestNatsEventLog(t)

	ev1 := makeNatsEvent("nats:batch:1:insert:0/1", `{"id": 1}`)
	ev2 := makeNatsEvent("nats:batch:2:insert:0/2", `{"id": 2}`)
	ev3 := makeNatsEvent("nats:batch:1:insert:0/1", `{"id": 1}`) // duplicate of ev1

	seqs, err := el.AppendBatch([]*event.ChangeEvent{ev1, ev2, ev3})
	require.NoError(t, err)
	require.Len(t, seqs, 3, "AppendBatch must return a slice of length N")

	require.Greater(t, seqs[0], uint64(0), "first event (non-duplicate) must have seq >= 1")
	require.Greater(t, seqs[1], uint64(0), "second event (non-duplicate) must have seq >= 1")
	require.Equal(t, uint64(0), seqs[2], "third event (duplicate of ev1) must have seq=0")
}

// TestNatsEventLogClose verifies:
//   - Close() returns nil.
//   - Calling Close twice does not panic.
func TestNatsEventLogClose(t *testing.T) {
	el, err := eventlog.OpenNats(eventlog.NatsEventLogConfig{
		Server: eventlog.NatsServerConfig{
			ClientPort: -1,
			StoreDir:   t.TempDir(),
			SyncAlways: false,
		},
		NumPartitions: 64,
		Retention:     time.Hour,
	})
	require.NoError(t, err)

	require.NoError(t, el.Close(), "Close() must return nil")

	// Calling Close a second time must not panic.
	require.NotPanics(t, func() {
		_ = el.Close()
	})
}

// TestNatsEventLogPartitionIsolation verifies:
//   - Events written to partition A are not returned by ReadPartition for partition B.
//
// We use PartitionOf to identify two keys that hash to different partitions, then
// verify cross-partition reads return no events from the other partition.
func TestNatsEventLogPartitionIsolation(t *testing.T) {
	el := openTestNatsEventLog(t)

	// Pick two keys that definitely hash to different partitions.
	// Scan through IDs until we find a pair that diverges.
	keyA := `{"id": 1}`
	partA := eventlog.PartitionOf(json.RawMessage(keyA), 64)

	var keyB string
	var partB uint32
	for i := 2; i <= 1000; i++ {
		candidate := fmt.Sprintf(`{"id": %d}`, i)
		p := eventlog.PartitionOf(json.RawMessage(candidate), 64)
		if p != partA {
			keyB = candidate
			partB = p
			break
		}
	}
	if keyB == "" {
		t.Skip("could not find two keys in different partitions — extremely unlikely with 64 partitions")
	}

	evA := makeNatsEvent("nats:isolation:A:insert:0/1", keyA)
	evB := makeNatsEvent("nats:isolation:B:insert:0/1", keyB)

	seqA, err := el.Append(evA)
	require.NoError(t, err)
	require.Greater(t, seqA, uint64(0))

	seqB, err := el.Append(evB)
	require.NoError(t, err)
	require.Greater(t, seqB, uint64(0))

	ctx := context.Background()

	// evA should NOT appear in partition B's results.
	entriesB, err := el.ReadPartition(ctx, partB, 1, 100)
	require.NoError(t, err)
	for _, e := range entriesB {
		require.NotEqual(t, evA.IdempotencyKey, e.Event.IdempotencyKey,
			"event from partition A must not appear in partition B reads")
	}

	// evB should NOT appear in partition A's results.
	entriesA, err := el.ReadPartition(ctx, partA, 1, 100)
	require.NoError(t, err)
	for _, e := range entriesA {
		require.NotEqual(t, evB.IdempotencyKey, e.Event.IdempotencyKey,
			"event from partition B must not appear in partition A reads")
	}
}
