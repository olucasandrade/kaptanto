package mongodb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	mongodb "github.com/kaptanto/kaptanto/internal/source/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodrv "go.mongodb.org/mongo-driver/v2/mongo"
)

// ---- Fake implementations -----------------------------------------------

type fakeStore struct {
	saved    map[string]string
	loadErr  error
	saveErr  error
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
	appendErr   error
	appendCalls int
}

func (f *fakeEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	f.appendCalls++
	return 1, f.appendErr
}

func (f *fakeEventLog) ReadPartition(_ context.Context, _ uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	return nil, nil
}

func (f *fakeEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	f.appendCalls += len(evs)
	seqs := make([]uint64, len(evs))
	for i := range seqs {
		seqs[i] = 1
	}
	return seqs, f.appendErr
}

func (f *fakeEventLog) Close() error { return nil }

// fakeIter is an injectable change stream iterator.
type fakeIter struct {
	events      []bson.Raw
	idx         int
	err         error
	resumeToken bson.Raw
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

func (f *fakeIter) Decode(v any) error {
	if f.idx >= len(f.events) {
		return errors.New("no more events")
	}
	raw := f.events[f.idx]
	f.idx++
	// decode into the target; the connector expects bson.Raw
	if rp, ok := v.(*bson.Raw); ok {
		*rp = raw
		return nil
	}
	return bson.Unmarshal(raw, v)
}

func (f *fakeIter) ResumeToken() bson.Raw { return f.resumeToken }
func (f *fakeIter) Err() error            { return f.err }
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
	store.saved["mongo_token"] = `{"_data":"resumehere"}`

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
