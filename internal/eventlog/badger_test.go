// Package eventlog_test provides black-box TDD tests for the BadgerEventLog implementation.
// All tests use the external test package to ensure only exported symbols are tested.
package eventlog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
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
	defer func() { _ = el.Close() }()

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

func TestBadgerEventLog_AppendBatch(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()

	// Empty batch is a no-op.
	seqs, err := el.AppendBatch(nil)
	require.NoError(t, err)
	assert.Nil(t, seqs)

	evs := []*event.ChangeEvent{
		makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`),
		makeEvent("src:public.t:2:insert:0/2", `{"id": 2}`),
		makeEvent("src:public.t:3:insert:0/3", `{"id": 3}`),
	}
	seqs, err = el.AppendBatch(evs)
	require.NoError(t, err)
	require.Len(t, seqs, 3)
	for i, s := range seqs {
		assert.Greater(t, s, uint64(0), "seq[%d] should be > 0", i)
	}

	// Re-appending the same batch returns the duplicate sentinel (0) for each.
	dupSeqs, err := el.AppendBatch(evs)
	require.NoError(t, err)
	require.Len(t, dupSeqs, 3)
	for i, s := range dupSeqs {
		assert.Equal(t, uint64(0), s, "duplicate seq[%d] should be 0 (LOG-03)", i)
	}
}

func TestBadgerEventLog_Ping(t *testing.T) {
	// 64 partitions: the canonical topology (BKF-02 FNV-1a hashing contract).
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()
	assert.NoError(t, el.Ping(), "Ping on an open event log should succeed")
}

// TestBadgerEventLog_Dedup verifies that a second Append with the same IdempotencyKey
// is silently skipped and ReadPartition returns exactly one entry (LOG-03).
func TestBadgerEventLog_Dedup(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()

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
	defer func() { _ = el.Close() }()

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
	defer func() { _ = el.Close() }()

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
	defer func() { _ = el.Close() }()

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

// TestBadgerEventLog_ReopenPreservesDedupAndEntries verifies the unit half of
// the CHK-01 crash-recovery story: after Close() and a fresh Open() on the
// same directory, (a) previously appended entries are still readable via
// ReadPartition, (b) re-appending the same IdempotencyKeys returns the dup
// sentinel (seq=0) rather than creating duplicates, and (c) a brand-new
// IdempotencyKey continues the sequence rather than colliding with pre-reopen
// seqs.
func TestBadgerEventLog_ReopenPreservesDedupAndEntries(t *testing.T) {
	dir := t.TempDir()

	el, err := eventlog.Open(dir, 64, time.Hour)
	require.NoError(t, err)
	elClosed := false
	defer func() {
		if !elClosed {
			_ = el.Close()
		}
	}()

	evs := []*event.ChangeEvent{
		makeEvent("src:public.t:1:insert:0/1", `{"id": 1}`),
		makeEvent("src:public.t:2:insert:0/2", `{"id": 2}`),
		makeEvent("src:public.t:3:insert:0/3", `{"id": 3}`),
	}

	var firstSeqs []uint64
	for _, ev := range evs {
		seq, err := el.Append(ev)
		require.NoError(t, err)
		assert.Greater(t, seq, uint64(0), "first append of %s should return seq > 0", ev.IdempotencyKey)
		firstSeqs = append(firstSeqs, seq)
	}

	require.NoError(t, el.Close())
	elClosed = true

	// Simulate a crash-restart: reopen the same directory.
	el2, err := eventlog.Open(dir, 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el2.Close() }()

	ctx := context.Background()

	// (a) All 3 original entries must still be present after reopen, with
	// their original seq preserved (not just the key).
	found := map[string]bool{}
	foundSeqs := map[string]uint64{}
	for p := uint32(0); p < 64; p++ {
		entries, err := el2.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		for _, e := range entries {
			found[e.Event.IdempotencyKey] = true
			foundSeqs[e.Event.IdempotencyKey] = e.Seq
		}
	}
	for i, ev := range evs {
		assert.True(t, found[ev.IdempotencyKey], "entry %s must survive reopen", ev.IdempotencyKey)
		assert.Equal(t, firstSeqs[i], foundSeqs[ev.IdempotencyKey],
			"seq for %s must be preserved across reopen", ev.IdempotencyKey)
	}
	assert.Len(t, found, 3, "reopen must not introduce extra entries")

	// (b) Re-appending the same events (as a re-sent WAL/change-stream source
	// would after a crash, per CHK-01) must be deduped: seq=0 for every one.
	for i, ev := range evs {
		seq, err := el2.Append(ev)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), seq,
			"re-append of %s after reopen must return dup sentinel seq=0 (original seq was %d)",
			ev.IdempotencyKey, firstSeqs[i])
	}

	// (c) A genuinely new IdempotencyKey must still get a fresh, positive seq —
	// the sequence counter itself must also have survived the reopen.
	newEv := makeEvent("src:public.t:4:insert:0/4", `{"id": 4}`)
	newSeq, err := el2.Append(newEv)
	require.NoError(t, err)
	assert.Greater(t, newSeq, uint64(0), "new event after reopen should get a fresh seq > 0")

	newFound := false
	for p := uint32(0); p < 64; p++ {
		entries, err := el2.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		for _, e := range entries {
			if e.Event.IdempotencyKey == newEv.IdempotencyKey {
				newFound = true
				assert.Equal(t, newSeq, e.Seq)
			}
		}
	}
	assert.True(t, newFound, "new event after reopen must be retrievable via ReadPartition")
}

// TestReadPartition_RawPopulated verifies that ReadPartition populates LogEntry.Raw
// with the exact bytes that were stored by Append (raw-bytes-passthrough fix).
func TestReadPartition_RawPopulated(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() {
		if err := el.Close(); err != nil {
			t.Errorf("close eventlog: %v", err)
		}
	}()

	ev := makeEvent("src:raw:1:insert:0/1", `{"id": 1}`)
	_, err = el.Append(ev)
	require.NoError(t, err)

	// Find the entry across all partitions.
	ctx := context.Background()
	var found *eventlog.LogEntry
	for p := uint32(0); p < 64; p++ {
		entries, err := el.ReadPartition(ctx, p, 0, 100)
		require.NoError(t, err)
		for i := range entries {
			if entries[i].Event.IdempotencyKey == ev.IdempotencyKey {
				e := entries[i]
				found = &e
				break
			}
		}
		if found != nil {
			break
		}
	}
	require.NotNil(t, found, "event not found after Append")

	// Raw must be non-empty and valid JSON.
	require.NotEmpty(t, found.Raw, "LogEntry.Raw must be populated by ReadPartition")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(found.Raw, &decoded), "LogEntry.Raw must be valid JSON")

	// Raw must round-trip to the same key value as the stored event.
	rawKey, ok := decoded["key"]
	require.True(t, ok, "key field must exist in Raw JSON")
	rawKeyJSON, err := json.Marshal(rawKey)
	require.NoError(t, err)
	assert.JSONEq(t, string(ev.Key), string(rawKeyJSON), "Raw JSON must contain the correct key")
}

// TestBadgerEventLog_ConcurrentAppendAndRead exercises the actual production
// access pattern: a source writing events (Append) while the router reads all
// partitions (ReadPartition) concurrently. Every writer uses distinct keys and
// distinct IdempotencyKeys, so no dedup contention is expected — this test is
// purely about the race detector observing contended Append/Append and
// Append/ReadPartition access without corrupting state.
func TestBadgerEventLog_ConcurrentAppendAndRead(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()

	const (
		numWriters      = 8
		eventsPerWriter = 200
		numReaders      = 4
	)

	var writersWG sync.WaitGroup
	for w := 0; w < numWriters; w++ {
		writersWG.Add(1)
		go func(writerID int) {
			defer writersWG.Done()
			for i := 0; i < eventsPerWriter; i++ {
				keyJSON, err := json.Marshal(map[string]int{"writer": writerID, "i": i})
				if err != nil {
					t.Errorf("marshal key: %v", err)
					return
				}
				idKey := fmt.Sprintf("src:public.t:concurrent:%d:%d", writerID, i)
				ev := makeEvent(idKey, string(keyJSON))
				seq, err := el.Append(ev)
				if err != nil {
					t.Errorf("Append: %v", err)
					continue
				}
				if seq == 0 {
					t.Errorf("Append for a unique key returned duplicate sentinel seq=0")
				}
			}
		}(w)
	}

	// Readers poll all 64 partitions concurrently with the writers above,
	// stopping once the writers finish.
	stopReaders := make(chan struct{})
	var readersWG sync.WaitGroup
	for r := 0; r < numReaders; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			ctx := context.Background()
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				for p := uint32(0); p < 64; p++ {
					if _, err := el.ReadPartition(ctx, p, 0, eventsPerWriter*numWriters+10); err != nil {
						t.Errorf("ReadPartition(%d): %v", p, err)
					}
				}
			}
		}()
	}

	writersWG.Wait()
	close(stopReaders)
	readersWG.Wait()

	// Final verification: every appended event is readable, and per-partition
	// sequence numbers are gapless (no duplicates written, no errors, so every
	// leased sequence number in a partition must have been consumed exactly once).
	ctx := context.Background()
	perPartitionSeqs := make(map[uint32][]uint64)
	total := 0
	for p := uint32(0); p < 64; p++ {
		entries, err := el.ReadPartition(ctx, p, 0, numWriters*eventsPerWriter+10)
		require.NoError(t, err)
		total += len(entries)
		for _, e := range entries {
			perPartitionSeqs[p] = append(perPartitionSeqs[p], e.Seq)
		}
	}
	assert.Equal(t, numWriters*eventsPerWriter, total, "all appended events must be readable after concurrent Append/ReadPartition")

	for p, seqs := range perPartitionSeqs {
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		assert.Equal(t, uint64(1), seqs[0], "partition %d: first seq should be 1", p)
		for i := 1; i < len(seqs); i++ {
			assert.Equal(t, seqs[i-1]+1, seqs[i], "partition %d: seq gap between %d and %d", p, seqs[i-1], seqs[i])
		}
	}
}

// TestBadgerEventLog_ConcurrentAppendBatchDedupRace fires the SAME batch of
// events (identical IdempotencyKeys) from multiple goroutines simultaneously.
// Badger's optimistic concurrency control may return a conflict error for a
// losing transaction, so callers retry — mirroring how a real caller would
// treat AppendBatch under contention. The invariant under test: no matter how
// many goroutines race to write a given IdempotencyKey, exactly one of them
// observes a non-zero (real) sequence number for it; every other observer
// sees the duplicate sentinel (0). This is the "dedup is race-safe" guarantee
// (LOG-03) under true concurrent access.
func TestBadgerEventLog_ConcurrentAppendBatchDedupRace(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 64, time.Hour)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()

	const (
		numGoroutines = 8
		numKeys       = 20
		maxAttempts   = 50
	)

	events := make([]*event.ChangeEvent, numKeys)
	for i := 0; i < numKeys; i++ {
		keyJSON, err := json.Marshal(map[string]int{"id": i})
		require.NoError(t, err)
		events[i] = makeEvent(fmt.Sprintf("src:public.t:dedup-race:%d", i), string(keyJSON))
	}

	var mu sync.Mutex
	successCounts := make(map[string]int, numKeys)

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			var seqs []uint64
			var err error
			for attempt := 0; attempt < maxAttempts; attempt++ {
				seqs, err = el.AppendBatch(events)
				if err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil {
				t.Errorf("AppendBatch: did not succeed within %d attempts: %v", maxAttempts, err)
				return
			}

			mu.Lock()
			for i, s := range seqs {
				if s != 0 {
					successCounts[events[i].IdempotencyKey]++
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for i := 0; i < numKeys; i++ {
		key := events[i].IdempotencyKey
		assert.Equal(t, 1, successCounts[key], "key %q must have exactly one non-zero seq across all concurrent AppendBatch calls", key)
	}
}

// TestBadgerEventLog_ConcurrentAppendDuringTTLExpiry exercises Append racing
// against Badger's TTL/compaction machinery (LOG-04) — the single-threaded
// pattern TestBadgerEventLog_TTLExpiry already exercises, but now under
// concurrent writers while entries begin expiring mid-run. The goal is
// exposing any race between Append and Badger's background GC/compaction
// goroutines; there is no assertion on exact surviving count since TTL races
// by design, only that no panic, deadlock, or error occurs.
func TestBadgerEventLog_ConcurrentAppendDuringTTLExpiry(t *testing.T) {
	el, err := eventlog.Open(t.TempDir(), 8, 5*time.Millisecond)
	require.NoError(t, err)
	defer func() { _ = el.Close() }()

	const (
		numWriters      = 8
		eventsPerWriter = 100
	)

	var wg sync.WaitGroup
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < eventsPerWriter; i++ {
				keyJSON, err := json.Marshal(map[string]int{"writer": writerID, "i": i})
				if err != nil {
					t.Errorf("marshal key: %v", err)
					return
				}
				idKey := fmt.Sprintf("src:public.t:ttlrace:%d:%d", writerID, i)
				ev := makeEvent(idKey, string(keyJSON))
				if _, err := el.Append(ev); err != nil {
					t.Errorf("Append: %v", err)
				}
				if i%10 == 0 {
					time.Sleep(time.Millisecond) // let some entries cross the 5ms TTL mid-run
				}
			}
		}(w)
	}
	wg.Wait()

	// A final read across all partitions must complete cleanly even though
	// entries were expiring concurrently with the writes above.
	ctx := context.Background()
	for p := uint32(0); p < 8; p++ {
		if _, err := el.ReadPartition(ctx, p, 0, 10000); err != nil {
			t.Errorf("ReadPartition(%d) after TTL race: %v", p, err)
		}
	}
}
