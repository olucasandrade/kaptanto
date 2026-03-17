package mongodb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	mongodb "github.com/kaptanto/kaptanto/internal/source/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// mockWatermarkChecker implements a controllable ShouldEmit function for testing.
type mockWatermarkChecker struct {
	// shouldEmitByKey maps JSON key string to emit decision.
	// Default (key not in map) returns true.
	shouldEmitByKey map[string]bool
}

func (m *mockWatermarkChecker) ShouldEmit(_ context.Context, _ string, pk json.RawMessage, _ uint64) (bool, error) {
	if m.shouldEmitByKey == nil {
		return true, nil
	}
	if v, ok := m.shouldEmitByKey[string(pk)]; ok {
		return v, nil
	}
	return true, nil
}

// fakeSnapshotEventLog is an EventLog that records appended events.
type fakeSnapshotEventLog struct {
	appended []*event.ChangeEvent
}

func (f *fakeSnapshotEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	f.appended = append(f.appended, ev)
	return uint64(len(f.appended)), nil
}

func (f *fakeSnapshotEventLog) ReadPartition(_ context.Context, _ uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	return nil, nil
}

func (f *fakeSnapshotEventLog) Close() error { return nil }

// buildRawDoc builds a minimal bson.Raw document with an _id and a field.
func buildRawDoc(idHex string, field, val string) bson.Raw {
	doc := bson.D{
		{Key: "_id", Value: bson.ObjectID{}},
		{Key: field, Value: val},
	}
	raw, _ := bson.Marshal(doc)
	return raw
}

// TestMongoSnapshot_SkipsWatermarkedRows verifies that rows where
// WatermarkChecker.ShouldEmit returns false are NOT passed to appendFn.
func TestMongoSnapshot_SkipsWatermarkedRows(t *testing.T) {
	idGen := event.NewIDGenerator()
	var appended []*event.ChangeEvent

	appendFn := func(_ context.Context, ev *event.ChangeEvent) error {
		appended = append(appended, ev)
		return nil
	}

	// Build 3 raw BSON documents. We inject them via the findFn.
	docs := []bson.Raw{
		buildRawDoc("doc1", "name", "alpha"),
		buildRawDoc("doc2", "name", "beta"),
		buildRawDoc("doc3", "name", "gamma"),
	}

	// findFn returns our 3 docs.
	findFn := func(_ context.Context, _ string, _ any, _ ...any) ([]bson.Raw, error) {
		return docs, nil
	}

	// WatermarkChecker: we control which docs pass. Since we can't easily predict
	// the serialized key JSON for these docs, we use a checker that accepts all
	// and separately test the skip path with a counter-based checker.
	skipCount := 0
	wc := &countingWatermarkChecker{
		skipEvery: 2, // skip doc index 0 and 2 (i.e., positions 0, 2)
		skipped:   &skipCount,
	}

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"col1"},
		SourceID:    "test",
	}

	snap := mongodb.NewMongoSnapshot(cfg, nil, wc, idGen, appendFn)
	snap.SetFindFn(findFn)
	snap.SetSnapshotLSN(12345)

	err := snap.Run(context.Background())
	require.NoError(t, err)

	// Only 1 doc should be appended (doc index 1) + 1 OpControl event.
	// The 2 skipped docs are NOT appended (not even the control event is skipped).
	opReadCount := 0
	opControlCount := 0
	for _, ev := range appended {
		if ev.Operation == event.OpRead {
			opReadCount++
		}
		if ev.Operation == event.OpControl {
			opControlCount++
		}
	}
	assert.Equal(t, 1, opReadCount, "exactly 1 doc should pass the watermark check")
	assert.Equal(t, 1, opControlCount, "exactly 1 control event per collection")
}

// TestMongoSnapshot_ControlEventAfterCollection verifies that after all rows
// for a collection are processed, an OpControl event with metadata["event"]="snapshot_complete"
// is appended.
func TestMongoSnapshot_ControlEventAfterCollection(t *testing.T) {
	idGen := event.NewIDGenerator()
	var appended []*event.ChangeEvent

	appendFn := func(_ context.Context, ev *event.ChangeEvent) error {
		appended = append(appended, ev)
		return nil
	}

	docs := []bson.Raw{
		buildRawDoc("doc1", "x", "1"),
	}
	findFn := func(_ context.Context, _ string, _ any, _ ...any) ([]bson.Raw, error) {
		return docs, nil
	}

	wc := &mockWatermarkChecker{} // all pass

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"orders"},
		SourceID:    "test",
	}

	snap := mongodb.NewMongoSnapshot(cfg, nil, wc, idGen, appendFn)
	snap.SetFindFn(findFn)
	snap.SetSnapshotLSN(1)

	err := snap.Run(context.Background())
	require.NoError(t, err)

	require.NotEmpty(t, appended, "at least one event must be appended")

	// Last event must be OpControl with table="orders" and metadata["event"]="snapshot_complete".
	last := appended[len(appended)-1]
	assert.Equal(t, event.OpControl, last.Operation, "last event must be OpControl")
	assert.Equal(t, "orders", last.Table, "control event table must match collection name")
	require.NotNil(t, last.Metadata, "control event must have metadata")
	assert.Equal(t, "snapshot_complete", last.Metadata["event"], `metadata["event"] must be "snapshot_complete"`)
}

// TestMongoSnapshot_AllDocsWatermarked verifies that if all docs are
// watermarked (ShouldEmit=false), only the control event is appended.
func TestMongoSnapshot_AllDocsWatermarked(t *testing.T) {
	idGen := event.NewIDGenerator()
	var appended []*event.ChangeEvent

	appendFn := func(_ context.Context, ev *event.ChangeEvent) error {
		appended = append(appended, ev)
		return nil
	}

	docs := []bson.Raw{
		buildRawDoc("doc1", "x", "1"),
		buildRawDoc("doc2", "x", "2"),
	}
	findFn := func(_ context.Context, _ string, _ any, _ ...any) ([]bson.Raw, error) {
		return docs, nil
	}

	// All docs fail watermark check.
	wc := &alwaysSkipWatermarkChecker{}

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"events"},
		SourceID:    "test",
	}

	snap := mongodb.NewMongoSnapshot(cfg, nil, wc, idGen, appendFn)
	snap.SetFindFn(findFn)
	snap.SetSnapshotLSN(99)

	err := snap.Run(context.Background())
	require.NoError(t, err)

	// Only OpControl event should be appended.
	require.Len(t, appended, 1, "only control event expected when all docs are watermarked")
	assert.Equal(t, event.OpControl, appended[0].Operation)
}

// TestMongoSnapshot_ContextCancellation verifies that Run returns
// context.Canceled when the context is cancelled.
func TestMongoSnapshot_ContextCancellation(t *testing.T) {
	idGen := event.NewIDGenerator()

	appendFn := func(_ context.Context, ev *event.ChangeEvent) error {
		return nil
	}

	findFn := func(_ context.Context, _ string, _ any, _ ...any) ([]bson.Raw, error) {
		return nil, nil
	}

	wc := &mockWatermarkChecker{}

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"col1"},
		SourceID:    "test",
	}

	snap := mongodb.NewMongoSnapshot(cfg, nil, wc, idGen, appendFn)
	snap.SetFindFn(findFn)
	snap.SetSnapshotLSN(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	err := snap.Run(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- Helper watermark checkers ---

// countingWatermarkChecker skips every Nth call (0-indexed).
type countingWatermarkChecker struct {
	call      int
	skipEvery int // skip when call index % skipEvery == 0 (for indices 0, 2, ...)
	skipped   *int
}

func (c *countingWatermarkChecker) ShouldEmit(_ context.Context, _ string, _ json.RawMessage, _ uint64) (bool, error) {
	idx := c.call
	c.call++
	// Skip indices 0, 2, 4, ... (every other one starting from 0)
	if idx%2 == 0 {
		*c.skipped++
		return false, nil
	}
	return true, nil
}

// alwaysSkipWatermarkChecker always returns false.
type alwaysSkipWatermarkChecker struct{}

func (a *alwaysSkipWatermarkChecker) ShouldEmit(_ context.Context, _ string, _ json.RawMessage, _ uint64) (bool, error) {
	return false, nil
}
