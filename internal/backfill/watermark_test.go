// Package backfill_test covers the indexed watermark path added to replace
// the O(rows*events) per-row full-partition scan (see watermark.go). All
// tests use the external test package to exercise only exported symbols.
package backfill_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- partitionedEventLog: a real, partition-respecting, observable EventLog fake ---
//
// mockEventLog (backfill_test.go) ignores the requested partition entirely,
// which is fine for the pre-existing scan tests but doesn't exercise
// per-partition routing or the AppendObserver contract. partitionedEventLog
// does both: events are bucketed by eventlog.PartitionOf exactly as
// BadgerEventLog does, and it implements eventlog.AppendObservable so
// WatermarkChecker's indexed path (and its observer-ordering guarantee) can
// be tested against something structurally faithful to production, not just
// against itself.
type partitionedEventLog struct {
	mu            sync.Mutex
	numPartitions uint32
	partitions    map[uint32][]eventlog.LogEntry
	seqCounters   map[uint32]uint64
	observers     map[int]eventlog.AppendObserver
	nextObsID     int

	// readHook, if set, is invoked synchronously at the start of every
	// ReadPartition call (before taking the lock), letting tests inject a
	// concurrent append at a precise point in a StartTable scan.
	readHook func(partition uint32, fromSeq uint64)
}

var (
	_ eventlog.EventLog         = (*partitionedEventLog)(nil)
	_ eventlog.AppendObservable = (*partitionedEventLog)(nil)
	_ eventlog.AppendObserver   = (*backfill.WatermarkChecker)(nil)
)

func newPartitionedEventLog(numPartitions uint32) *partitionedEventLog {
	return &partitionedEventLog{
		numPartitions: numPartitions,
		partitions:    make(map[uint32][]eventlog.LogEntry),
		seqCounters:   make(map[uint32]uint64),
		observers:     make(map[int]eventlog.AppendObserver),
	}
}

func (p *partitionedEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	seqs, err := p.AppendBatch([]*event.ChangeEvent{ev})
	if err != nil {
		return 0, err
	}
	return seqs[0], nil
}

func (p *partitionedEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	if len(evs) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	seqs := make([]uint64, len(evs))
	for i, ev := range evs {
		partition := eventlog.PartitionOf(ev.Key, p.numPartitions)
		p.seqCounters[partition]++
		seq := p.seqCounters[partition]
		seqs[i] = seq
		p.partitions[partition] = append(p.partitions[partition], eventlog.LogEntry{
			Seq: seq, PartitionID: partition, Event: ev,
		})
	}
	observers := make([]eventlog.AppendObserver, 0, len(p.observers))
	for _, obs := range p.observers {
		observers = append(observers, obs)
	}
	p.mu.Unlock()

	// Notify observers synchronously, after the "durable" write and before
	// AppendBatch returns — mirroring BadgerEventLog's contract exactly, since
	// WatermarkChecker's correctness depends on that ordering, not on this
	// fake's internals.
	for _, obs := range observers {
		obs.ObserveAppend(evs, seqs)
	}
	return seqs, nil
}

func (p *partitionedEventLog) ReadPartition(_ context.Context, partition uint32, fromSeq uint64, limit int) ([]eventlog.LogEntry, error) {
	if p.readHook != nil {
		p.readHook(partition, fromSeq)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var page []eventlog.LogEntry
	for _, e := range p.partitions[partition] {
		if e.Seq < fromSeq {
			continue
		}
		page = append(page, e)
		if len(page) >= limit {
			break
		}
	}
	return page, nil
}

func (p *partitionedEventLog) Close() error { return nil }

func (p *partitionedEventLog) RegisterObserver(obs eventlog.AppendObserver) func() {
	p.mu.Lock()
	id := p.nextObsID
	p.nextObsID++
	p.observers[id] = obs
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.observers, id)
			p.mu.Unlock()
		})
	}
}

func mkEvent(table, keyJSON string, lsn uint64) *event.ChangeEvent {
	return &event.ChangeEvent{
		Table:    table,
		Key:      json.RawMessage(keyJSON),
		Metadata: map[string]any{"lsn": fmt.Sprintf("0/%X", lsn)},
	}
}

// --- (a) Property test: indexed path agrees with the scan path ---
//
// The "old" scan implementation is still live in watermark.go as
// shouldEmitScan — ShouldEmit falls back to it automatically whenever
// StartTable was never called. That means the pre-existing behaviour is
// reachable through the very same exported ShouldEmit method, just via a
// different call sequence, so this test can compare "old" vs "new" without
// needing access to unexported symbols: for each seeded fixture, a checker
// that never calls StartTable exercises the original scan; a checker that
// does call StartTable exercises the new index. Both operate over the same
// partition-respecting fake event log.
func TestWatermarkChecker_IndexedMatchesScan_Property(t *testing.T) {
	ctx := context.Background()
	const numPartitions = 8
	tables := []string{"orders", "customers", "invoices"}

	rng := rand.New(rand.NewSource(42))

	for run := 0; run < 200; run++ {
		el := newPartitionedEventLog(numPartitions)

		// Seed a random assortment of events across a handful of keys and
		// tables, with LSNs randomly above/below the snapshot cutoff.
		const snapshotLSN = uint64(1000)
		numKeys := 1 + rng.Intn(12)
		keys := make([]string, numKeys)
		for i := range keys {
			keys[i] = fmt.Sprintf(`{"id":%d}`, i)
		}

		numEvents := rng.Intn(60)
		for i := 0; i < numEvents; i++ {
			table := tables[rng.Intn(len(tables))]
			key := keys[rng.Intn(len(keys))]
			// LSNs spread both sides of snapshotLSN.
			lsn := uint64(rng.Intn(2000))
			_, err := el.Append(mkEvent(table, key, lsn))
			require.NoError(t, err)
		}

		targetTable := tables[rng.Intn(len(tables))]
		targetKey := json.RawMessage(keys[rng.Intn(len(keys))])

		// Old path: no StartTable call, forces the scan fallback.
		scanChecker := backfill.NewWatermarkChecker(el, numPartitions)
		wantEmit, err := scanChecker.ShouldEmit(ctx, targetTable, targetKey, snapshotLSN)
		require.NoError(t, err)

		// New path: StartTable builds the index first.
		idxChecker := backfill.NewWatermarkChecker(el, numPartitions)
		require.NoError(t, idxChecker.StartTable(ctx, targetTable, snapshotLSN))
		gotEmit, err := idxChecker.ShouldEmit(ctx, targetTable, targetKey, snapshotLSN)
		require.NoError(t, err)
		idxChecker.FinishTable(targetTable)

		assert.Equalf(t, wantEmit, gotEmit,
			"run %d: indexed and scan paths disagree for table=%s key=%s (numEvents=%d)",
			run, targetTable, string(targetKey), numEvents)
	}
}

// --- (b) Race test: concurrent AppendBatch + ShouldEmit ---
func TestWatermarkChecker_ConcurrentAppendAndShouldEmit_Race(t *testing.T) {
	ctx := context.Background()
	const numPartitions = 16
	const table = "orders"
	el := newPartitionedEventLog(numPartitions)

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	require.NoError(t, checker.StartTable(ctx, table, 0))
	defer checker.FinishTable(table)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: keeps appending new events for a rotating set of keys.
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
			key := fmt.Sprintf(`{"id":%d}`, i%50)
			_, err := el.AppendBatch([]*event.ChangeEvent{mkEvent(table, key, uint64(1000+i))})
			assert.NoError(t, err)
			i++
		}
	}()

	// Readers: hammer ShouldEmit concurrently with the writer.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				key := json.RawMessage(fmt.Sprintf(`{"id":%d}`, (i+id)%50))
				_, err := checker.ShouldEmit(ctx, table, key, 0)
				assert.NoError(t, err)
			}
		}(r)
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// --- (c) Benchmark: O(1) indexed vs O(W) scan, 100k-entry partition ---
//
// Both benchmarks share the same 100k-event fixture (a single partition,
// since numPartitions=1 forces every key into partition 0, matching the
// "100k-entry partition" framing used by the pre-existing scan test). The
// scan benchmark repeats the pre-fix behaviour; the indexed benchmark builds
// the index once outside the timed loop, then times only ShouldEmit calls.
func setupBenchFixture(b *testing.B, n int) (*partitionedEventLog, string, json.RawMessage, uint64) {
	b.Helper()
	const table = "orders"
	el := newPartitionedEventLog(1) // single partition holds everything
	for i := 0; i < n-1; i++ {
		// Filler events for a different key than the one we'll query, all
		// with a low LSN so they never match — this is the worst case for
		// the scan path (it must walk every one of them).
		_, err := el.Append(mkEvent(table, `{"id":999999}`, 1))
		require.NoError(b, err)
	}
	// One superseding event for the target key at the very end (highest
	// seq) — the worst case: the scan can't stop early.
	targetKey := json.RawMessage(`{"id":1}`)
	_, err := el.Append(mkEvent(table, string(targetKey), 200))
	require.NoError(b, err)
	return el, table, targetKey, 100 // snapshotLSN = 100 < 200, so the target key is suppressed
}

func BenchmarkShouldEmit_ScanPath_100kPartition(b *testing.B) {
	el, table, targetKey, snapshotLSN := setupBenchFixture(b, 100_000)
	checker := backfill.NewWatermarkChecker(el, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emit, err := checker.ShouldEmit(context.Background(), table, targetKey, snapshotLSN)
		if err != nil || emit {
			b.Fatalf("unexpected result: emit=%v err=%v", emit, err)
		}
	}
}

func BenchmarkShouldEmit_IndexedPath_100kPartition(b *testing.B) {
	el, table, targetKey, snapshotLSN := setupBenchFixture(b, 100_000)
	checker := backfill.NewWatermarkChecker(el, 1)
	if err := checker.StartTable(context.Background(), table, snapshotLSN); err != nil {
		b.Fatalf("StartTable: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emit, err := checker.ShouldEmit(context.Background(), table, targetKey, snapshotLSN)
		if err != nil || emit {
			b.Fatalf("unexpected result: emit=%v err=%v", emit, err)
		}
	}
}

// --- (d) Integration-style test: snapshot with concurrent updates ---
//
// Simulates the exact scenario watermarks exist for: a snapshot is running
// (StartTable already active) and a WAL event lands for a row that hasn't
// been read yet. The stale snapshot read for that row must still be
// suppressed.
func TestWatermarkChecker_ConcurrentUpdateDuringSnapshot_StaleReadSuppressed(t *testing.T) {
	ctx := context.Background()
	const numPartitions = 8
	const table = "orders"
	const snapshotLSN = uint64(100)

	el := newPartitionedEventLog(numPartitions)
	// Some pre-existing, non-superseding history so the index build has real
	// work to do.
	for i := 0; i < 10; i++ {
		_, err := el.Append(mkEvent(table, fmt.Sprintf(`{"id":%d}`, i), 10))
		require.NoError(t, err)
	}

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	require.NoError(t, checker.StartTable(ctx, table, snapshotLSN))
	defer checker.FinishTable(table)

	targetKey := json.RawMessage(`{"id":42}`)

	// Before the concurrent update, nothing supersedes this row.
	emit, err := checker.ShouldEmit(ctx, table, targetKey, snapshotLSN)
	require.NoError(t, err)
	assert.True(t, emit, "no WAL event yet — snapshot row should be emitted")

	// A WAL event lands for this exact row while the snapshot is "still
	// running" (StartTable is still active), with an LSN beyond the
	// snapshot's watermark.
	_, err = el.Append(mkEvent(table, string(targetKey), snapshotLSN+100))
	require.NoError(t, err)

	// The now-stale snapshot read for this row must be suppressed.
	emit, err = checker.ShouldEmit(ctx, table, targetKey, snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emit, "WAL event superseding the row must suppress the stale snapshot read")
}

// TestWatermarkChecker_ObserverAttachedBeforeScan_NoMissedAppend is the
// regression test for the ordering fix described in StartTable's doc
// comment: the observer must be attached before the index scan begins, so an
// append landing in an already-scanned partition, mid-build, is still
// captured (via the observer) instead of silently escaping suppression.
//
// readHook fires the "concurrent" append the moment the scan moves on to the
// partition after the target key's partition — guaranteeing that partition
// has already been fully read by the scan and the ONLY way the event can end
// up in the index is via the observer registered ahead of the scan.
func TestWatermarkChecker_ObserverAttachedBeforeScan_NoMissedAppend(t *testing.T) {
	ctx := context.Background()
	const numPartitions = 8
	const table = "orders"
	const snapshotLSN = uint64(100)

	targetKey := json.RawMessage(`{"id":7}`)
	targetPartition := eventlog.PartitionOf(targetKey, numPartitions)
	require.Less(t, targetPartition, uint32(numPartitions-1),
		"test fixture assumption: target key must not be in the last partition")

	el := newPartitionedEventLog(numPartitions)

	var fired sync.Once
	el.readHook = func(partition uint32, fromSeq uint64) {
		if partition == targetPartition+1 && fromSeq == 0 {
			fired.Do(func() {
				// This must land in targetPartition, which the scan has
				// already fully read by this point.
				_, err := el.Append(mkEvent(table, string(targetKey), snapshotLSN+50))
				require.NoError(t, err)
			})
		}
	}

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	require.NoError(t, checker.StartTable(ctx, table, snapshotLSN))
	defer checker.FinishTable(table)

	emit, err := checker.ShouldEmit(ctx, table, targetKey, snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emit,
		"append injected into an already-scanned partition mid-build must still be "+
			"captured by the pre-attached observer, and must suppress the stale row")
}

// --- Memory cap fallback ---

func TestWatermarkChecker_OverCap_FallsBackToScan(t *testing.T) {
	ctx := context.Background()
	const numPartitions = 4
	const table = "orders"
	const snapshotLSN = uint64(0)

	el := newPartitionedEventLog(numPartitions)
	// Three distinct superseding keys — cap of 2 forces overCap.
	for i := 0; i < 3; i++ {
		_, err := el.Append(mkEvent(table, fmt.Sprintf(`{"id":%d}`, i), snapshotLSN+1))
		require.NoError(t, err)
	}

	checker := backfill.NewWatermarkChecker(el, numPartitions)
	checker.SetMaxIndexKeys(2)
	require.NoError(t, checker.StartTable(ctx, table, snapshotLSN))
	defer checker.FinishTable(table)

	// Regardless of which keys made it into the index before the cap was
	// hit, every one of the three superseded keys must still be correctly
	// suppressed via the scan fallback.
	for i := 0; i < 3; i++ {
		key := json.RawMessage(fmt.Sprintf(`{"id":%d}`, i))
		emit, err := checker.ShouldEmit(ctx, table, key, snapshotLSN)
		require.NoError(t, err)
		assert.False(t, emit, "key %d should be suppressed via cap fallback", i)
	}
}

// failingReadEventLog fails ReadPartition on failOnPartition, simulating a
// mid-scan error (or, by the same code path, a cancelled context) partway
// through StartTable's one-time index build. A superseding event sits on
// failOnPartition+1 — a partition StartTable never reaches before the error.
type failingReadEventLog struct {
	failOnPartition        uint32
	supersedingKey         json.RawMessage
	supersedingTable       string
	supersedingLSNOnFailP1 string
}

func (f *failingReadEventLog) Append(ev *event.ChangeEvent) (uint64, error) { return 1, nil }
func (f *failingReadEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	return make([]uint64, len(evs)), nil
}
func (f *failingReadEventLog) Close() error { return nil }
func (f *failingReadEventLog) ReadPartition(_ context.Context, partition uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	if partition == f.failOnPartition {
		return nil, fmt.Errorf("simulated read failure on partition %d", partition)
	}
	if partition == f.failOnPartition+1 {
		return []eventlog.LogEntry{
			{Seq: 1, Event: &event.ChangeEvent{
				Table:    f.supersedingTable,
				Key:      f.supersedingKey,
				Metadata: map[string]any{"lsn": f.supersedingLSNOnFailP1},
			}},
		}, nil
	}
	return nil, nil
}

// TestWatermarkChecker_StartTableError_DoesNotLeaveTrustedPartialIndex
// verifies that when StartTable's scan fails partway through, it does not
// leave the partial (incomplete) index registered for ShouldEmit to
// silently trust. A partial index is strictly worse than the intentional
// overCap fallback: ShouldEmit cannot distinguish "index is complete" from
// "index stopped halfway due to an error" by inspecting the index alone, so
// an unremoved partial index would make a WAL event in an unscanned
// partition invisible — exactly the "seen by neither path" failure mode
// StartTable's ordering guarantee is supposed to rule out.
func TestWatermarkChecker_StartTableError_DoesNotLeaveTrustedPartialIndex(t *testing.T) {
	el := &failingReadEventLog{
		failOnPartition:        0,
		supersedingTable:       "orders",
		supersedingKey:         json.RawMessage(`{"id":"1"}`),
		supersedingLSNOnFailP1: "0/C8", // 200, supersedes snapshotLSN=100
	}
	w := backfill.NewWatermarkChecker(el, 4)

	err := w.StartTable(context.Background(), "orders", 100)
	require.Error(t, err, "expected StartTable to surface the simulated read failure")

	emit, err := w.ShouldEmit(context.Background(), "orders", json.RawMessage(`{"id":"1"}`), 100)
	require.NoError(t, err)
	assert.False(t, emit,
		"a row superseded by a WAL event in a partition StartTable never reached "+
			"before failing must still be suppressed via the scan fallback — the "+
			"failed StartTable must not leave a partial index for ShouldEmit to trust")
}

// TestWatermarkChecker_NatsEventLog_IndexedPathSuppressesStaleRead proves the
// cluster-mode wiring this fix adds: NatsEventLog implements AppendObservable,
// NewWatermarkChecker registers the observer, and a post-StartTable Append
// (after PubAck) is indexed so ShouldEmit suppresses without paging ReadPartition.
func TestWatermarkChecker_NatsEventLog_IndexedPathSuppressesStaleRead(t *testing.T) {
	// One partition: NATS ReadPartition still uses FetchMaxWait(2s) on empty
	// pages, so a 64-partition StartTable (~128s) exceeds CI's 120s timeout.
	const numPartitions = uint32(1)
	el, err := eventlog.OpenNats(eventlog.NatsEventLogConfig{
		Server: eventlog.NatsServerConfig{
			ClientPort: -1,
			StoreDir:   t.TempDir(),
			SyncAlways: false,
		},
		NumPartitions: numPartitions,
		Retention:     time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = el.Close() })

	_, ok := any(el).(eventlog.AppendObservable)
	require.True(t, ok, "NatsEventLog must implement AppendObservable")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const table = "orders"
	const snapshotLSN = uint64(100)
	checker := backfill.NewWatermarkChecker(el, numPartitions)
	defer checker.Close()
	require.NoError(t, checker.StartTable(ctx, table, snapshotLSN))
	defer checker.FinishTable(table)

	targetKey := json.RawMessage(`{"id":42}`)
	emit, err := checker.ShouldEmit(ctx, table, targetKey, snapshotLSN)
	require.NoError(t, err)
	assert.True(t, emit, "no WAL event yet — snapshot row should be emitted")

	ev := &event.ChangeEvent{
		Table:          table,
		Key:            targetKey,
		IdempotencyKey: "nats:wm:42:insert:0/C8",
		Metadata:       map[string]any{"lsn": fmt.Sprintf("0/%X", snapshotLSN+100)},
	}
	seq, err := el.Append(ev)
	require.NoError(t, err)
	require.Greater(t, seq, uint64(0))

	emit, err = checker.ShouldEmit(ctx, table, targetKey, snapshotLSN)
	require.NoError(t, err)
	assert.False(t, emit, "post-PubAck observer must index the WAL event so ShouldEmit suppresses without scanning")
}
