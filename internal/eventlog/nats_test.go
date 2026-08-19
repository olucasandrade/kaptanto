// Package eventlog_test provides black-box TDD tests for the NatsEventLog implementation.
// All tests use an in-process single-node NATS server for isolation and speed.
package eventlog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
)

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
	// identify the exact partition rather than scanning all 64.
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

func listStreamConsumers(t *testing.T, el *eventlog.NatsEventLog) []*jetstream.ConsumerInfo {
	t.Helper()
	jsctx, err := jetstream.New(el.Conn())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := jsctx.Stream(ctx, "kaptanto-events")
	require.NoError(t, err)
	lister := stream.ListConsumers(ctx)
	var infos []*jetstream.ConsumerInfo
	for info := range lister.Info() {
		infos = append(infos, info)
	}
	require.NoError(t, lister.Err())
	return infos
}

// TestNatsEventLogReadPartitionEmptyIsFast verifies empty ReadPartition returns
// without waiting FetchMaxWait(2s). Scanning all 64 partitions must finish well
// under the old 64x2s bound so idle notify can wake a partition.
func TestNatsEventLogReadPartitionEmptyIsFast(t *testing.T) {
	el := openTestNatsEventLog(t)
	ctx := context.Background()
	start := time.Now()
	entries, err := el.ReadPartition(ctx, 0, 1, 10)
	require.NoError(t, err)
	require.Empty(t, entries)
	elapsed := time.Since(start)
	require.Less(t, elapsed, 500*time.Millisecond,
		"empty ReadPartition took %s; must not wait FetchMaxWait(2s)", elapsed)
}

// TestNatsEventLogReadPartitionRawPopulated verifies msg.Data is copied into Raw.
func TestNatsEventLogReadPartitionRawPopulated(t *testing.T) {
	el := openTestNatsEventLog(t)
	key := `{"id": 1}`
	partition := eventlog.PartitionOf(json.RawMessage(key), 64)
	ev := makeNatsEvent("nats:raw:1:insert:0/1", key)
	_, err := el.Append(ev)
	require.NoError(t, err)

	entries, err := el.ReadPartition(context.Background(), partition, 1, 10)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	found := entries[0]
	require.NotEmpty(t, found.Raw, "LogEntry.Raw must be populated by ReadPartition")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(found.Raw, &decoded))
	require.Equal(t, ev.IdempotencyKey, found.Event.IdempotencyKey)
	// Mutating Raw must not affect a subsequent read (copied, not aliased).
	found.Raw[0] ^= 0xff
	entries2, err := el.ReadPartition(context.Background(), partition, 1, 10)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(entries2[0].Raw, &decoded))
}

// TestNatsEventLogReadPartitionReusesConsumer verifies repeated empty polls
// do not create a new JetStream consumer each time.
func TestNatsEventLogReadPartitionReusesConsumer(t *testing.T) {
	el := openTestNatsEventLog(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		entries, err := el.ReadPartition(ctx, 0, 1, 10)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	require.Len(t, listStreamConsumers(t, el), 1)
}

// TestNatsEventLogReleasePartitionDeletesConsumer verifies ownership release
// removes the long-lived pull consumer.
func TestNatsEventLogReleasePartitionDeletesConsumer(t *testing.T) {
	el := openTestNatsEventLog(t)
	_, err := el.ReadPartition(context.Background(), 0, 1, 10)
	require.NoError(t, err)
	require.Len(t, listStreamConsumers(t, el), 1)

	el.ReleasePartition(0)
	require.Empty(t, listStreamConsumers(t, el))
}

// TestNatsEventLogReadPartitionRewindReReads verifies a lower fromSeq recreates
// the pull consumer so failed delivery can re-read the same window.
func TestNatsEventLogReadPartitionRewindReReads(t *testing.T) {
	el := openTestNatsEventLog(t)
	key := `{"id": 1}`
	partition := eventlog.PartitionOf(json.RawMessage(key), 64)
	for i := 1; i <= 3; i++ {
		ev := makeNatsEvent(fmt.Sprintf("nats:rewind:%d:insert:0/%d", i, i), key)
		_, err := el.Append(ev)
		require.NoError(t, err)
	}
	ctx := context.Background()
	first, err := el.ReadPartition(ctx, partition, 1, 10)
	require.NoError(t, err)
	require.Len(t, first, 3)

	again, err := el.ReadPartition(ctx, partition, 1, 10)
	require.NoError(t, err)
	require.Len(t, again, 3)
	require.Equal(t, first[0].Seq, again[0].Seq)
	require.Equal(t, first[2].Seq, again[2].Seq)
}

// TestNatsEventLog_ImplementsAppendObservable is the compile/runtime gate
// that cluster backfill depends on: without this interface, WatermarkChecker
// falls back to paging ReadPartition on every ShouldEmit.
func TestNatsEventLog_ImplementsAppendObservable(t *testing.T) {
	el := openTestNatsEventLog(t)
	_, ok := any(el).(eventlog.AppendObservable)
	require.True(t, ok, "NatsEventLog must implement AppendObservable")
}

// TestNatsEventLog_RegisterObserver_SyncAfterPubAck verifies the
// AppendObserver contract for the NATS EventLog: observers fire
// synchronously after a successful (non-duplicate) PubAck and before
// Append/AppendBatch returns. Duplicates (seq==0) are never observed.
func TestNatsEventLog_RegisterObserver_SyncAfterPubAck(t *testing.T) {
	el := openTestNatsEventLog(t)

	obs := &recordingObserver{}
	unregister := el.RegisterObserver(obs)

	ev1 := makeNatsEvent("nats:obs:1:insert:0/1", `{"id": 1}`)
	seq1, err := el.Append(ev1)
	require.NoError(t, err)
	require.Greater(t, seq1, uint64(0))
	require.Equal(t, []string{ev1.IdempotencyKey}, obs.allKeys(),
		"observer must see the event before Append returns (synchronous after PubAck)")

	evs := []*event.ChangeEvent{
		makeNatsEvent("nats:obs:2:insert:0/2", `{"id": 2}`),
		makeNatsEvent("nats:obs:3:insert:0/3", `{"id": 3}`),
	}
	seqs, err := el.AppendBatch(evs)
	require.NoError(t, err)
	require.Greater(t, seqs[0], uint64(0))
	require.Greater(t, seqs[1], uint64(0))

	assert.ElementsMatch(t, []string{ev1.IdempotencyKey, evs[0].IdempotencyKey, evs[1].IdempotencyKey}, obs.allKeys(),
		"observer must see every non-duplicate event from Append and AppendBatch")

	_, err = el.Append(ev1)
	require.NoError(t, err)
	_, err = el.AppendBatch(evs)
	require.NoError(t, err)
	assert.Len(t, obs.allKeys(), 3, "duplicate appends must not generate additional observer calls")

	unregister()
	ev4 := makeNatsEvent("nats:obs:4:insert:0/4", `{"id": 4}`)
	_, err = el.Append(ev4)
	require.NoError(t, err)
	assert.Len(t, obs.allKeys(), 3, "unregistered observer must not receive further calls")

	unregister()
}

// TestNatsEventLog_RegisterObserver_ConcurrentAppendAndUnregister exercises
// RegisterObserver's synchronization under concurrent Append while another
// goroutine registers/unregisters.
func TestNatsEventLog_RegisterObserver_ConcurrentAppendAndUnregister(t *testing.T) {
	el := openTestNatsEventLog(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			ev := makeNatsEvent(fmt.Sprintf("nats:obs-race:%d:insert:0/%d", i, i), fmt.Sprintf(`{"id": %d}`, i))
			_, err := el.Append(ev)
			assert.NoError(t, err)
			i++
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			obs := &recordingObserver{}
			unregister := el.RegisterObserver(obs)
			unregister()
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// selfUnregisterObserver calls its unregister func from ObserveAppend. Used to
// prove notifyObservers does not hold obsMu across callbacks.
type selfUnregisterObserver struct {
	unregister func()
	mu         sync.Mutex
	keys       []string
}

func (s *selfUnregisterObserver) ObserveAppend(evs []*event.ChangeEvent, _ []uint64) {
	s.mu.Lock()
	for _, ev := range evs {
		s.keys = append(s.keys, ev.IdempotencyKey)
	}
	s.mu.Unlock()
	if s.unregister != nil {
		s.unregister()
	}
}

func (s *selfUnregisterObserver) allKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.keys))
	copy(out, s.keys)
	return out
}

// TestNatsEventLog_RegisterObserver_SelfUnregisterDuringCallback is the
// deadlock regression: unregister takes obsMu.Lock, so ObserveAppend must not
// run while notifyObservers still holds obsMu.RLock.
func TestNatsEventLog_RegisterObserver_SelfUnregisterDuringCallback(t *testing.T) {
	el := openTestNatsEventLog(t)

	self := &selfUnregisterObserver{}
	self.unregister = el.RegisterObserver(self)
	kept := &recordingObserver{}
	unregisterKept := el.RegisterObserver(kept)

	ev1 := makeNatsEvent("nats:self-unreg:1:insert:0/1", `{"id": 1}`)
	done := make(chan error, 1)
	go func() {
		_, err := el.Append(ev1)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "Append must complete when an observer unregisters itself")
	case <-time.After(5 * time.Second):
		t.Fatal("Append deadlocked: observer unregistered while obsMu was held")
	}

	assert.Equal(t, []string{ev1.IdempotencyKey}, self.allKeys())
	assert.Equal(t, []string{ev1.IdempotencyKey}, kept.allKeys(),
		"snapshot observers captured before unregister still receive this append")

	ev2 := makeNatsEvent("nats:self-unreg:2:insert:0/2", `{"id": 2}`)
	_, err := el.Append(ev2)
	require.NoError(t, err)
	assert.Equal(t, []string{ev1.IdempotencyKey}, self.allKeys(),
		"self-unregistered observer must not see later appends")
	assert.ElementsMatch(t, []string{ev1.IdempotencyKey, ev2.IdempotencyKey}, kept.allKeys())
	unregisterKept()
}
