// Package eventlog_test provides black-box TDD tests for the BadgerEventLog implementation.
// All tests use the external test package to ensure only exported symbols are tested.
package eventlog_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeEvent(idempotencyKey string, keyJSON string) *event.ChangeEvent {
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

// TestBadgerEventLog_AppendAndRead verifies Append returns a positive sequence
// and the event is retrievable via ReadPartition (LOG-01).
func TestBadgerEventLog_AppendAndRead(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer el.Close()

	ev := makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`)
	seq, err := el.Append(ev)
	require.NoError(t, err)
	assert.Greater(t, seq, uint64(0), "first Append should return a sequence > 0")

	// ReadPartition: use the partition the event landed in. We need to check all partitions
	// since we don't expose which partition was chosen — iterate all 64 and find the event.
	ctx := context.Background()
	found := false
	for p := uint32(0); p < 64; p++ {
		entries, err := el.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		for _, e := range entries {
			if e.Event.IdempotencyKey == ev.IdempotencyKey {
				found = true
				assert.Equal(t, seq, e.Seq, "returned Seq should match Append seq")
			}
		}
	}
	assert.True(t, found, "event should be retrievable from ReadPartition")
}

// TestBadgerEventLog_Dedup verifies that a second Append with the same IdempotencyKey
// is silently skipped and ReadPartition returns exactly one entry (LOG-03).
func TestBadgerEventLog_Dedup(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer el.Close()

	ev := makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`)

	seq1, err := el.Append(ev)
	require.NoError(t, err)
	assert.Greater(t, seq1, uint64(0))

	seq2, err := el.Append(ev)
	require.NoError(t, err)
	// seq2 must be either 0 (sentinel) or equal to seq1 (implementation choice).
	assert.True(t, seq2 == 0 || seq2 == seq1, "duplicate Append must return 0 or original seq, got %d", seq2)

	ctx := context.Background()
	totalEntries := 0
	for p := uint32(0); p < 64; p++ {
		entries, err := el.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		for _, e := range entries {
			if e.Event.IdempotencyKey == ev.IdempotencyKey {
				totalEntries++
			}
		}
	}
	assert.Equal(t, 1, totalEntries, "dedup: exactly one entry should exist for the same IdempotencyKey")
}

// TestBadgerEventLog_Partitioning verifies that partitioning is deterministic (LOG-02):
// two events with the same key always land in the same partition;
// two events with different keys that hash to different partitions land in different ones.
func TestBadgerEventLog_Partitioning(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer el.Close()

	ev1a := makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`)
	ev1b := makeEvent("src:public.t:1:insert:0/2", `{"id": 1}`) // same key, different op+pos

	_, err = el.Append(ev1a)
	require.NoError(t, err)
	_, err = el.Append(ev1b)
	require.NoError(t, err)

	ctx := context.Background()

	// Find partitions containing each event.
	findPartition := func(idempotencyKey string) int {
		for p := uint32(0); p < 64; p++ {
			entries, err := el.ReadPartition(ctx, p, 0, 100)
			require.NoError(t, err)
			for _, e := range entries {
				if e.Event.IdempotencyKey == idempotencyKey {
					return int(p)
				}
			}
		}
		return -1
	}

	p1a := findPartition("src:public.t:1:insert:0/1")
	p1b := findPartition("src:public.t:1:insert:0/2")
	assert.NotEqual(t, -1, p1a, "event 1a should be found in some partition")
	assert.NotEqual(t, -1, p1b, "event 1b should be found in some partition")
	assert.Equal(t, p1a, p1b, "same key must land in the same partition")

	// Now find a key that lands in a different partition (brute force a different key).
	var differentPartition int
	for i := 2; i <= 1000; i++ {
		keyJSON, _ := json.Marshal(map[string]int{"id": i})
		evN := makeEvent("src:public.t:N:insert:0/N", string(keyJSON))
		_, err = el.Append(evN)
		require.NoError(t, err)
		p := findPartition("src:public.t:N:insert:0/N")
		if p != p1a {
			differentPartition = p
			break
		}
	}
	// It's astronomically unlikely that all 999 IDs hash to the same partition as id=1 with 64 partitions.
	assert.NotEqual(t, p1a, differentPartition, "different keys should be able to land in different partitions")
}

// TestBadgerEventLog_TTLExpiry verifies that events written with a very short TTL
// are absent from ReadPartition after expiry (LOG-04).
func TestBadgerEventLog_TTLExpiry(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, 1*time.Nanosecond)
	require.NoError(t, err)
	defer el.Close()

	ev := makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`)
	_, err = el.Append(ev)
	require.NoError(t, err)

	// Wait for TTL to expire.
	time.Sleep(10 * time.Millisecond)

	ctx := context.Background()
	totalEntries := 0
	for p := uint32(0); p < 64; p++ {
		entries, err := el.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		totalEntries += len(entries)
	}
	assert.Equal(t, 0, totalEntries, "events should be absent after TTL expiry")
}

// TestBadgerEventLog_ReadPartitionFromSeq verifies that ReadPartition respects fromSeq
// and returns only entries with Seq >= fromSeq.
func TestBadgerEventLog_ReadPartitionFromSeq(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 1, time.Hour) // 1 partition: all events go to partition 0
	require.NoError(t, err)
	defer el.Close()

	// Write 5 events to partition 0 (1 partition so everything goes there).
	var seqs []uint64
	for i := 1; i <= 5; i++ {
		keyJSON := json.RawMessage(`{"id": 1}`) // same key → same partition (only 1 partition)
		ev := makeEvent("src:public.t:fromseq:insert:0/"+string(rune('0'+i)), string(keyJSON))
		ev.IdempotencyKey = "src:public.t:fromseq:insert:0/" + string(rune('0'+i)) // unique per event
		seq, err := el.Append(ev)
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	require.Len(t, seqs, 5, "should have 5 sequence numbers")
	// seqs[1] is the 2nd event's seq.
	fromSeq := seqs[1]

	ctx := context.Background()
	entries, err := el.ReadPartition(ctx, 0, fromSeq, 100)
	require.NoError(t, err)

	for _, e := range entries {
		assert.GreaterOrEqual(t, e.Seq, fromSeq, "all returned entries must have Seq >= fromSeq")
	}
	assert.GreaterOrEqual(t, len(entries), 4, "should return at least entries from seq2 onward")
}

// TestBadgerEventLog_Close verifies that Close completes without error.
func TestBadgerEventLog_Close(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)

	ev := makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`)
	_, err = el.Append(ev)
	require.NoError(t, err)

	err = el.Close()
	assert.NoError(t, err, "Close should complete without error")
}
