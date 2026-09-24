package mongodb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
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

// buildRawDocWithID builds a bson.Raw document with an explicit `_id`.
func buildRawDocWithID(id any, field, val string) bson.Raw {
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: field, Value: val},
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

// parseAfterID extracts the `$gt` bound from a keyset filter produced by fetchPage.
func parseAfterID(filter any) any {
	d, ok := filter.(bson.D)
	if !ok || len(d) == 0 {
		return nil
	}
	if d[0].Key != "_id" {
		return nil
	}
	inner, ok := d[0].Value.(bson.D)
	if !ok || len(inner) == 0 || inner[0].Key != "$gt" {
		return nil
	}
	return inner[0].Value
}

func extractTestDocID(raw bson.Raw) (any, error) {
	idVal, err := raw.LookupErr("_id")
	if err != nil {
		return nil, err
	}
	var id any
	if err := idVal.Unmarshal(&id); err != nil {
		return nil, err
	}
	return id, nil
}

// idGreater compares string `_id` values used by the paging tests.
func idGreater(a, b any) bool {
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return as > bs
	}
	return fmt.Sprintf("%v", a) > fmt.Sprintf("%v", b)
}

// pagingFindFn returns a findFn that pages over all with keyset `$gt` + limit.
// It records call count and peak window size for assertions.
func pagingFindFn(all []bson.Raw) (fn func(context.Context, string, any, ...any) ([]bson.Raw, error), calls *int, peak *int) {
	calls = new(int)
	peak = new(int)
	fn = func(_ context.Context, _ string, filter any, opts ...any) ([]bson.Raw, error) {
		*calls++
		limit := int64(len(all))
		if len(opts) > 0 {
			if n, ok := opts[0].(int64); ok {
				limit = n
			}
		}
		after := parseAfterID(filter)
		out := make([]bson.Raw, 0, limit)
		for _, raw := range all {
			id, err := extractTestDocID(raw)
			if err != nil {
				continue
			}
			if after != nil && !idGreater(id, after) {
				continue
			}
			out = append(out, raw)
			if int64(len(out)) >= limit {
				break
			}
		}
		if len(out) > *peak {
			*peak = len(out)
		}
		return out, nil
	}
	return fn, calls, peak
}

// TestMongoSnapshot_KeysetWindows_DoesNotMaterializeAll verifies that a large
// collection is consumed in bounded `_id` windows: findFn is called more than
// once and peak buffering stays at the window size, not N.
func TestMongoSnapshot_KeysetWindows_DoesNotMaterializeAll(t *testing.T) {
	idGen := event.NewIDGenerator()
	var appended []*event.ChangeEvent
	appendFn := func(_ context.Context, ev *event.ChangeEvent) error {
		appended = append(appended, ev)
		return nil
	}

	const n = 10
	const batch = 3
	docs := make([]bson.Raw, n)
	for i := 0; i < n; i++ {
		docs[i] = buildRawDocWithID(fmt.Sprintf("id-%02d", i), "x", fmt.Sprintf("%d", i))
	}
	findFn, calls, peak := pagingFindFn(docs)

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"big"},
		SourceID:    "test",
	}
	snap := mongodb.NewMongoSnapshot(cfg, nil, &mockWatermarkChecker{}, idGen, appendFn)
	snap.SetFindFn(findFn)
	snap.SetSnapshotLSN(1)
	snap.SetBatchSize(batch)

	require.NoError(t, snap.Run(context.Background()))

	assert.Greater(t, *calls, 1, "findFn must be called more than once for keyset windows")
	assert.LessOrEqual(t, *peak, batch, "peak buffering must be one window, not N docs")
	assert.Equal(t, batch, *peak, "peak should equal the configured window size")

	opRead := 0
	for _, ev := range appended {
		if ev.Operation == event.OpRead {
			opRead++
		}
	}
	assert.Equal(t, n, opRead, "all docs must still be emitted across windows")
}

// TestMongoSnapshot_CrashMidSnapshot_ResumesAfterStoredID verifies that after
// a mid-snapshot failure the stored `_id` is used to resume past the last
// completed window.
func TestMongoSnapshot_CrashMidSnapshot_ResumesAfterStoredID(t *testing.T) {
	idGen := event.NewIDGenerator()
	store := newFakeStore()

	const n = 6
	const batch = 2
	docs := make([]bson.Raw, n)
	for i := 0; i < n; i++ {
		docs[i] = buildRawDocWithID(fmt.Sprintf("id-%02d", i), "x", fmt.Sprintf("%d", i))
	}

	cfg := mongodb.SnapshotConfig{
		Database:    "testdb",
		Collections: []string{"orders"},
		SourceID:    "test",
	}

	// First run: fail after emitting 3 OpRead events (mid second window).
	var firstRead int
	failAfter := 3
	appendFn1 := func(_ context.Context, ev *event.ChangeEvent) error {
		if ev.Operation == event.OpRead {
			firstRead++
			if firstRead > failAfter {
				return fmt.Errorf("simulated crash")
			}
		}
		return nil
	}
	findFn1, _, _ := pagingFindFn(docs)
	snap1 := mongodb.NewMongoSnapshot(cfg, nil, &mockWatermarkChecker{}, idGen, appendFn1)
	snap1.SetFindFn(findFn1)
	snap1.SetSnapshotLSN(1)
	snap1.SetBatchSize(batch)
	snap1.SetProgressStore(store)

	err := snap1.Run(context.Background())
	require.Error(t, err, "first run must fail mid-snapshot")

	// Progress must have been saved after the first completed window (id-01).
	progressKey := "test:orders:snapshot"
	stored, ok := store.saved[progressKey]
	require.True(t, ok && stored != "", "progress `_id` must be persisted before crash")
	assert.Contains(t, stored, "id-01", "stored cursor should be last `_id` of first completed window")

	// Second run: resume; must not re-emit docs from the completed window.
	var resumedIDs []string
	var resumeCalls int
	findFn2, _, _ := pagingFindFn(docs)
	wrappedFind := func(ctx context.Context, coll string, filter any, opts ...any) ([]bson.Raw, error) {
		resumeCalls++
		if resumeCalls == 1 {
			after := parseAfterID(filter)
			require.NotNil(t, after, "resume find must pass `_id > stored` filter")
			assert.Equal(t, "id-01", after)
		}
		return findFn2(ctx, coll, filter, opts...)
	}
	appendFn2 := func(_ context.Context, ev *event.ChangeEvent) error {
		if ev.Operation == event.OpRead {
			var key map[string]any
			require.NoError(t, json.Unmarshal(ev.Key, &key))
			if id, ok := key["_id"].(string); ok {
				resumedIDs = append(resumedIDs, id)
			}
		}
		return nil
	}
	snap2 := mongodb.NewMongoSnapshot(cfg, nil, &mockWatermarkChecker{}, idGen, appendFn2)
	snap2.SetFindFn(wrappedFind)
	snap2.SetSnapshotLSN(1)
	snap2.SetBatchSize(batch)
	snap2.SetProgressStore(store)

	require.NoError(t, snap2.Run(context.Background()))

	assert.NotContains(t, resumedIDs, "id-00", "must not re-emit docs before stored `_id`")
	assert.NotContains(t, resumedIDs, "id-01")
	assert.Contains(t, resumedIDs, "id-02")
	assert.Contains(t, resumedIDs, "id-05")
	assert.Equal(t, "", store.saved[progressKey], "progress cleared after successful snapshot")
}
