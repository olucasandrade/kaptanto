package mongodb_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodrv "go.mongodb.org/mongo-driver/v2/mongo"
)

// ---- Fake implementations -----------------------------------------------

type fakeStore struct {
	saved     map[string]string
	loadErr   error
	saveErr   error
	saveCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{saved: make(map[string]string)}
}

func (f *fakeStore) Save(_ context.Context, sourceID, token string) error {
	f.saveCalls++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved[sourceID] = token
	return nil
}

func (f *fakeStore) Load(_ context.Context, sourceID string) (string, error) {
	if f.loadErr != nil {
		return "", f.loadErr
	}
	return f.saved[sourceID], nil
}

func (f *fakeStore) Close() error { return nil }

type fakeEventLog struct {
	appendErr error

	mu sync.Mutex
	// appendCalls counts single-event Append invocations.
	appendCalls int

	// batchCalls counts AppendBatch invocations; batchSizes records the
	// length of evs on each call, in order. Together these let tests assert
	// that N buffered events were flushed as ONE AppendBatch call (batching
	// worked) rather than N separate calls (batching regressed to size 1).
	batchCalls int
	batchSizes []int
}

func (f *fakeEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	f.mu.Lock()
	f.appendCalls++
	f.mu.Unlock()
	return 1, f.appendErr
}

func (f *fakeEventLog) AppendCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendCalls
}

func (f *fakeEventLog) BatchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batchCalls
}

func (f *fakeEventLog) BatchSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.batchSizes))
	copy(out, f.batchSizes)
	return out
}

func (f *fakeEventLog) ReadPartition(_ context.Context, _ uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	return nil, nil
}

func (f *fakeEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	f.mu.Lock()
	f.batchCalls++
	f.batchSizes = append(f.batchSizes, len(evs))
	f.mu.Unlock()
	if f.appendErr != nil {
		return nil, f.appendErr
	}
	seqs := make([]uint64, len(evs))
	for i := range seqs {
		seqs[i] = 1
	}
	return seqs, nil
}

func (f *fakeEventLog) Close() error { return nil }

// fakeIter is an injectable change stream iterator.
type fakeIter struct {
	events      []bson.Raw
	idx         int
	err         error
	resumeToken bson.Raw

	// noLookahead forces TryNext to always report no buffered events, even
	// when more events remain — simulating a driver whose internal buffer
	// never holds more than the document Next just fetched. Used to prove
	// the low-rate path degenerates to today's per-event (batch-size-1)
	// behavior.
	noLookahead  bool
	tryNextCalls int

	// failDecodeAt contains event indices (0-based into f.events) for which
	// Decode should return an error, simulating an unparseable change-stream
	// document. The index is still advanced so the iterator behaves as if the
	// document was consumed.
	failDecodeAt map[int]struct{}
}

func (f *fakeIter) Next(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if f.err != nil {
		return false
	}
	if f.idx < len(f.events) {
		return true
	}
	return false
}

// TryNext mirrors Next's "is a document available" semantics against the
// same idx/events backing store, unless noLookahead forces it to report
// nothing is buffered (see field doc).
func (f *fakeIter) TryNext(ctx context.Context) bool {
	f.tryNextCalls++
	if ctx.Err() != nil {
		return false
	}
	if f.err != nil {
		return false
	}
	if f.noLookahead {
		return false
	}
	return f.idx < len(f.events)
}

func (f *fakeIter) Decode(v any) error {
	if f.idx >= len(f.events) {
		return errors.New("no more events")
	}
	raw := f.events[f.idx]
	// Advance idx even on a simulated decode failure: the real driver has
	// consumed the document even if it could not decode it.
	f.idx++
	if _, fail := f.failDecodeAt[f.idx-1]; fail {
		return errors.New("simulated decode failure")
	}
	// decode into the target; the connector expects bson.Raw
	if rp, ok := v.(*bson.Raw); ok {
		*rp = raw
		return nil
	}
	return bson.Unmarshal(raw, v)
}

// ResumeToken mirrors the real driver: it reflects the "_id" field of the
// LAST document Decode returned, not a fixed value. Tests that don't decode
// real change-stream documents (e.g. an empty fakeIter{}) can still set the
// resumeToken field directly, which is used as a fallback before any
// document has been decoded.
func (f *fakeIter) ResumeToken() bson.Raw {
	if f.idx == 0 || f.idx > len(f.events) {
		return f.resumeToken
	}
	var doc struct {
		ID bson.Raw `bson:"_id"`
	}
	if err := bson.Unmarshal(f.events[f.idx-1], &doc); err != nil {
		return f.resumeToken
	}
	return doc.ID
}

func (f *fakeIter) Err() error                    { return f.err }
func (f *fakeIter) Close(_ context.Context) error { return nil }

// ---- Tests ---------------------------------------------------------------

func TestConfig_ApplyDefaults_SourceID(t *testing.T) {
	cfg := mongodb.Config{Database: "testdb"}
	cfg.ApplyDefaults()
	assert.Equal(t, "mongo_default", cfg.SourceID)
}

func TestConfig_ApplyDefaults_SourceIDPreserved(t *testing.T) {
	cfg := mongodb.Config{Database: "testdb", SourceID: "myid"}
	cfg.ApplyDefaults()
	assert.Equal(t, "myid", cfg.SourceID)
}

func TestNew_RequiresDatabase(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	_, err := mongodb.New(mongodb.Config{}, store, idGen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database")
}

func TestNew_DelegatesEventLog(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	c, err := mongodb.New(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen)
	require.NoError(t, err)
	assert.False(t, c.HasEventLog(), "New should produce connector with nil eventLog")
}

func TestNewWithEventLog_StoresEventLog(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}
	c, err := mongodb.NewWithEventLog(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen, el)
	require.NoError(t, err)
	assert.True(t, c.HasEventLog(), "NewWithEventLog should store non-nil eventLog")
}

func TestNeedsSnapshot_FalseByDefault(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	c, err := mongodb.New(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen)
	require.NoError(t, err)
	assert.False(t, c.NeedsSnapshot())
}

func TestAppendAndQueue_SkipsEventLogWhenNil(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	c, err := mongodb.New(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen)
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		ID:             idGen.New(),
		Operation:      event.OpInsert,
		Table:          "col",
		IdempotencyKey: "key",
	}
	token := bson.Raw(`{"_data":"abc"}`)

	err = c.AppendAndQueue(context.Background(), ev, token)
	require.NoError(t, err)
	// store.Save must be called even without event log
	assert.Equal(t, 1, store.saveCalls)
}

func TestAppendAndQueue_CHK01_AppendFailPreventsTokenSave(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{appendErr: errors.New("disk full")}
	c, err := mongodb.NewWithEventLog(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen, el)
	require.NoError(t, err)

	ev := &event.ChangeEvent{
		ID:             idGen.New(),
		Operation:      event.OpInsert,
		Table:          "col",
		IdempotencyKey: "key",
	}
	token := bson.Raw(`{"_data":"abc"}`)

	appErr := c.AppendAndQueue(context.Background(), ev, token)
	require.Error(t, appErr)
	assert.Equal(t, 0, store.saveCalls, "store.Save must NOT be called if Append fails (CHK-01)")
}

func TestRun_TokenLoadedOnStart(t *testing.T) {
	store := newFakeStore()
	store.saved["mongo_token:c1"] = `{"_data":"resumehere"}`

	idGen := event.NewIDGenerator()

	var capturedToken bson.Raw
	watchFn := func(_ context.Context, _ string, token bson.Raw) (mongodb.ChangeStreamIter, error) {
		capturedToken = token
		// Return an iter that immediately returns context done
		return &fakeIter{}, nil
	}

	cfg := mongodb.Config{
		Database:    "db",
		Collections: []string{"c1"},
		SourceID:    "mongo_token",
	}

	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, nil, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)

	assert.NotNil(t, capturedToken, "watch must receive a non-nil resume token from store")
}

func TestRun_InvalidResumeToken_SetsNeedsSnapshot(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()

	// Build a command error with code 260 (InvalidResumeToken)
	invalidTokenErr := mongodrv.CommandError{Code: 260, Name: "InvalidResumeToken", Message: "resume token not found"}

	watchFn := func(_ context.Context, _ string, _ bson.Raw) (mongodb.ChangeStreamIter, error) {
		return &fakeIter{err: invalidTokenErr}, nil
	}

	cfg := mongodb.Config{
		Database:    "db",
		Collections: []string{"c1"},
	}

	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, nil, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	runErr := c.Run(ctx)
	assert.NoError(t, runErr, "Run must return nil on InvalidResumeToken")
	assert.True(t, c.NeedsSnapshot(), "NeedsSnapshot must be true after InvalidResumeToken")
}

func TestRun_ContextCancel_ReturnsContextCanceled(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()

	watchFn := func(_ context.Context, _ string, _ bson.Raw) (mongodb.ChangeStreamIter, error) {
		return &fakeIter{}, nil
	}

	cfg := mongodb.Config{
		Database:    "db",
		Collections: []string{"c1"},
	}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, nil, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	runErr := c.Run(ctx)
	assert.ErrorIs(t, runErr, context.Canceled)
}

// ---- Batching tests (perf: batch change-stream appends) ------------------

// buildInsertChangeDoc builds a synthetic MongoDB Change Stream "insert"
// document with a distinct resume token, suitable for driving fakeIter
// through the real NormalizeChangeEvent path exercised by consumeStream.
func buildInsertChangeDoc(t *testing.T, tokenData string, oid bson.ObjectID, status string) bson.Raw {
	t.Helper()
	resumeToken := bson.D{{Key: "_data", Value: tokenData}}
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "status", Value: status}}
	doc := bson.D{
		{Key: "_id", Value: resumeToken},
		{Key: "operationType", Value: "insert"},
		{Key: "clusterTime", Value: bson.Timestamp{T: uint32(time.Now().Unix()), I: 1}},
		{Key: "ns", Value: bson.D{{Key: "db", Value: "db"}, {Key: "coll", Value: "c1"}}},
		{Key: "documentKey", Value: bson.D{{Key: "_id", Value: oid}}},
		{Key: "fullDocument", Value: fullDoc},
	}
	b, err := bson.Marshal(doc)
	require.NoError(t, err)
	return bson.Raw(b)
}

// TestConsumeStream_BatchesBufferedEvents_SingleAppendBatchCall verifies the
// core batching behavior (Test Plan a): when TryNext reports N-1 further
// buffered events after a successful blocking Next, all N events are
// flushed through exactly ONE AppendBatch call, the checkpoint token is
// saved exactly once (the LAST event's token), and event order is preserved
// on the events channel.
func TestConsumeStream_BatchesBufferedEvents_SingleAppendBatchCall(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}

	oid1, oid2, oid3 := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	doc1 := buildInsertChangeDoc(t, "82AA01", oid1, "one")
	doc2 := buildInsertChangeDoc(t, "82AA02", oid2, "two")
	doc3 := buildInsertChangeDoc(t, "82AA03", oid3, "three")

	iter := &fakeIter{events: []bson.Raw{doc1, doc2, doc3}}
	watchFn := func(_ context.Context, _ string, _ bson.Raw) (mongodb.ChangeStreamIter, error) {
		return iter, nil
	}

	cfg := mongodb.Config{Database: "db", Collections: []string{"c1"}, SourceID: "src"}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, el, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx)
	}()

	// Run consumes the fake iter in a background goroutine, then spins
	// retrying watchFn until ctx is cancelled. Wait for the expected batch
	// flush before cancelling so the test stays deterministic under CI load.
	require.Eventually(t, func() bool {
		sizes := el.BatchSizes()
		return el.BatchCalls() == 1 && len(sizes) == 1 && sizes[0] == 3
	}, 5*time.Second, 5*time.Millisecond, "expected one AppendBatch flush covering all 3 buffered events")

	cancel()
	<-runDone

	// Exactly one AppendBatch call, covering all 3 buffered events.
	assert.Equal(t, 1, el.BatchCalls(), "3 buffered events must flush as exactly one AppendBatch call")
	sizes := el.BatchSizes()
	require.Len(t, sizes, 1)
	assert.Equal(t, 3, sizes[0], "the single AppendBatch call must cover all 3 events")
	assert.Equal(t, 0, el.AppendCalls(), "single-event Append must not be used for a multi-event batch")

	// Token saved exactly once, and it is doc3's token (the LAST event), not
	// doc1's or doc2's.
	assert.Equal(t, 1, store.saveCalls, "checkpoint token must be saved once per batch, not once per event")
	assert.Contains(t, store.saved["src:c1"], "82AA03", "saved token must be the LAST event's token")
	assert.NotContains(t, store.saved["src:c1"], "82AA01", "saved token must not be an earlier event's token")
	assert.NotContains(t, store.saved["src:c1"], "82AA02", "saved token must not be an earlier event's token")

	// Event order preserved on the channel.
	ch := c.Events()
	first := readTestEvent(t, ch)
	second := readTestEvent(t, ch)
	third := readTestEvent(t, ch)
	assert.Contains(t, string(first.After), "one")
	assert.Contains(t, string(second.After), "two")
	assert.Contains(t, string(third.After), "three")
}

// readTestEvent reads the next event off ch, failing the test on timeout.
func readTestEvent(t *testing.T, ch <-chan *event.ChangeEvent) *event.ChangeEvent {
	t.Helper()
	select {
	case ev := <-ch:
		require.NotNil(t, ev)
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event on channel")
		return nil
	}
}

// TestConsumeStream_NoLookahead_BehavesLikePerEventPath verifies the
// regression safety net (Test Plan c): when TryNext reports nothing
// buffered immediately after each blocking Next (noLookahead), the
// connector degenerates to exactly today's per-event path — one AppendBatch
// call per event, each of size 1, and one token save per event.
func TestConsumeStream_NoLookahead_BehavesLikePerEventPath(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}

	oid1, oid2 := bson.NewObjectID(), bson.NewObjectID()
	doc1 := buildInsertChangeDoc(t, "82BB01", oid1, "x")
	doc2 := buildInsertChangeDoc(t, "82BB02", oid2, "y")

	iter := &fakeIter{events: []bson.Raw{doc1, doc2}, noLookahead: true}
	watchFn := func(_ context.Context, _ string, _ bson.Raw) (mongodb.ChangeStreamIter, error) {
		return iter, nil
	}

	cfg := mongodb.Config{Database: "db", Collections: []string{"c1"}, SourceID: "src"}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, el, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return el.BatchCalls() == 2 && len(el.BatchSizes()) == 2
	}, 5*time.Second, 5*time.Millisecond, "expected two size-1 AppendBatch flushes when TryNext never reports lookahead")

	cancel()
	<-runDone

	assert.Equal(t, 2, el.BatchCalls(), "with no lookahead, each event must flush its own AppendBatch call")
	for _, sz := range el.BatchSizes() {
		assert.Equal(t, 1, sz, "each batch must contain exactly 1 event when TryNext never reports lookahead")
	}
	assert.GreaterOrEqual(t, store.saveCalls, 2, "token must be saved once per event when batches are size 1")
}

// TestAppendAndQueueBatch_CHK01_AppendBatchFailPreventsTokenSave extends the
// single-event CHK-01 test (TestAppendAndQueue_CHK01_AppendFailPreventsTokenSave)
// to the batch path (Test Plan a): if AppendBatch fails, the checkpoint
// token must NOT be saved, regardless of batch size.
func TestAppendAndQueueBatch_CHK01_AppendBatchFailPreventsTokenSave(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{appendErr: errors.New("disk full")}
	c, err := mongodb.NewWithEventLog(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen, el)
	require.NoError(t, err)

	evs := []*event.ChangeEvent{
		{ID: idGen.New(), Operation: event.OpInsert, Table: "col", IdempotencyKey: "key1"},
		{ID: idGen.New(), Operation: event.OpInsert, Table: "col", IdempotencyKey: "key2"},
	}
	lastToken := bson.Raw(`{"_data":"last"}`)

	batchErr := c.AppendAndQueueBatch(context.Background(), "c1", evs, lastToken)
	require.Error(t, batchErr)
	assert.Equal(t, 0, store.saveCalls, "store.Save must NOT be called if AppendBatch fails (CHK-01)")
}

// TestAppendAndQueueBatch_EmptyBatch_NoOp verifies AppendAndQueueBatch is a
// safe no-op for an empty batch (defensive: consumeStream already guards
// this, but the method must not misbehave if called directly).
func TestAppendAndQueueBatch_EmptyBatch_NoOp(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}
	c, err := mongodb.NewWithEventLog(mongodb.Config{Database: "db", Collections: []string{"c1"}}, store, idGen, el)
	require.NoError(t, err)

	err = c.AppendAndQueueBatch(context.Background(), "c1", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, store.saveCalls)
	assert.Equal(t, 0, el.BatchCalls())
}
// TestConsumeStream_DecodeFailureAdvancesCheckpoint verifies that a single
// unparseable change-stream document does not stall the connector: its resume
// token is still captured and the checkpoint advances past it, so subsequent
// valid documents are processed.
func TestConsumeStream_DecodeFailureAdvancesCheckpoint(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}

	oidGood := bson.NewObjectID()
	docBad := buildInsertChangeDoc(t, "BAD01", bson.NewObjectID(), "bad")
	docGood := buildInsertChangeDoc(t, "GOOD01", oidGood, "good")

	iter := &fakeIter{
		events:       []bson.Raw{docBad, docGood},
		failDecodeAt: map[int]struct{}{0: {}},
		noLookahead:  true,
	}
	watchFn := func(_ context.Context, _ string, _ bson.Raw) (mongodb.ChangeStreamIter, error) {
		return iter, nil
	}

	cfg := mongodb.Config{Database: "db", Collections: []string{"c1"}, SourceID: "src"}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, el, watchFn)
	require.NoError(t, err)
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = c.Run(ctx)

	// The bad document should not produce an event, but the checkpoint must
	// advance past it so the good document is still delivered. fakeStore keeps
	// only the latest token, so we verify the final token is the good document
	// and that two saves occurred (one for the failed doc, one for the good).
	assert.Equal(t, 2, store.saveCalls, "checkpoint must be saved for failed doc and again for good doc")
	assert.Contains(t, store.saved["src:c1"], "GOOD01", "final checkpoint must be the good document")

	ch := c.Events()
	ev := readTestEvent(t, ch)
	assert.Contains(t, string(ev.After), "good")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.MongoSkippedDocsTotal.WithLabelValues("decode")))
}

func TestRun_PerCollectionCheckpointKeys(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()

	var c1Token, c2Token bson.Raw
	watchFn := func(_ context.Context, coll string, token bson.Raw) (mongodb.ChangeStreamIter, error) {
		switch coll {
		case "c1":
			c1Token = token
		case "c2":
			c2Token = token
		}
		return &fakeIter{}, nil
	}

	store.saved["src:c1"] = `{"_data":"token_c1"}`
	store.saved["src:c2"] = `{"_data":"token_c2"}`

	cfg := mongodb.Config{
		Database:    "db",
		Collections: []string{"c1", "c2"},
		SourceID:    "src",
	}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, nil, watchFn)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)

	assert.NotNil(t, c1Token)
	assert.NotNil(t, c2Token)
	assert.Contains(t, string(c1Token), "token_c1")
	assert.Contains(t, string(c2Token), "token_c2")
}

func TestRun_ReconnectUsesUpdatedResumeToken(t *testing.T) {
	store := newFakeStore()
	idGen := event.NewIDGenerator()
	el := &fakeEventLog{}

	openCount := 0
	watchFn := func(_ context.Context, _ string, token bson.Raw) (mongodb.ChangeStreamIter, error) {
		openCount++
		if openCount == 1 {
			return &fakeIter{err: errors.New("stream reset")}, nil
		}
		if !strings.Contains(string(token), "updated") {
			t.Fatalf("reconnect got stale token %q", token)
		}
		return &fakeIter{}, nil
	}

	cfg := mongodb.Config{Database: "db", Collections: []string{"c1"}, SourceID: "src"}
	c, err := mongodb.NewWithWatchFn(cfg, store, idGen, el, watchFn)
	require.NoError(t, err)

	updated := bson.Raw(`{"_data":"updated_token"}`)
	ev := &event.ChangeEvent{ID: idGen.New(), Operation: event.OpInsert, Table: "c1", IdempotencyKey: "k"}
	require.NoError(t, c.AppendAndQueueBatch(context.Background(), "c1", []*event.ChangeEvent{ev}, updated))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Run(ctx)

	assert.GreaterOrEqual(t, openCount, 2, "expected reconnect after stream error")
}
