// Package mongodb implements the MongoDB snapshot for CDC re-snapshot with
// watermark coordination.
//
// MongoSnapshot iterates all configured collections in bounded `_id` keyset
// windows (never OFFSET — CLAUDE.md invariant 3 / BKF keyset discipline) and
// applies the WatermarkChecker before appending each event to ensure
// duplicates are suppressed (CLAUDE.md invariant 4).
package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/event"
	mongoparser "github.com/olucasandrade/kaptanto/internal/parser/mongodb"
)

// defaultSnapshotBatchSize caps how many documents are buffered per keyset
// window. Matches Postgres backfillBatchSize so memory stays bounded.
const defaultSnapshotBatchSize = 256

// WatermarkChecker is the interface for watermark deduplication during snapshot.
// *backfill.WatermarkChecker satisfies this interface.
type WatermarkChecker interface {
	ShouldEmit(ctx context.Context, table string, pk json.RawMessage, snapshotLSN uint64) (bool, error)
}

// IndexedWatermarkChecker is an optional extension of WatermarkChecker that
// supports building a per-table in-memory index for O(1) ShouldEmit lookups
// (perf fix: replaces a full-partition scan per row). *backfill.WatermarkChecker
// satisfies this interface. MongoSnapshot type-asserts wc against it so a
// WatermarkChecker that only implements ShouldEmit (e.g. a minimal test
// fake) keeps working via the plain scan/fallback behaviour.
type IndexedWatermarkChecker interface {
	WatermarkChecker
	// StartTable builds the in-memory index for table/collection so
	// subsequent ShouldEmit calls for it are O(1). See
	// backfill.WatermarkChecker.StartTable for the correctness ordering.
	StartTable(ctx context.Context, table string, snapshotLSN uint64) error
	// FinishTable releases the in-memory index for table/collection.
	FinishTable(table string)
}

// SnapshotConfig holds all parameters for a MongoDB snapshot.
type SnapshotConfig struct {
	// Database is the MongoDB database name.
	Database string

	// Collections lists the collections to snapshot.
	Collections []string

	// SourceID is the stable identifier used in event metadata.
	SourceID string
}

// MongoSnapshot snapshots all configured MongoDB collections into the Event Log.
// It enforces watermark coordination to prevent duplicate events when resuming
// a snapshot after an interrupted Change Stream (SRC-12).
type MongoSnapshot struct {
	cfg         SnapshotConfig
	client      *mongo.Client // nil in unit tests; real client in production
	wc          WatermarkChecker
	idGen       *event.IDGenerator
	appendFn    func(ctx context.Context, ev *event.ChangeEvent) error
	snapshotLSN uint64 // captured before snapshot begins (cluster time as uint64)

	// batchSize is the keyset window size. Zero means defaultSnapshotBatchSize.
	batchSize int

	// progressStore optionally persists the last processed `_id` per
	// collection so a mid-snapshot crash can resume. Distinct keys from
	// change-stream resume tokens (see progressKey). Nil disables resume.
	progressStore checkpoint.CheckpointStore

	// findFn allows test injection — defaults to real mongo Find when nil.
	// Each call must return at most one keyset window (honour the filter and
	// optional limit passed via opts); the snapshot processes that window
	// before requesting the next.
	findFn func(ctx context.Context, coll string, filter any, opts ...any) ([]bson.Raw, error)
}

// NewMongoSnapshot creates a MongoSnapshot. client may be nil for unit tests
// (inject findFn via SetFindFn). wc must be non-nil; appendFn is the
// Event Log append path (typically MongoDBConnector.AppendAndQueue).
func NewMongoSnapshot(
	cfg SnapshotConfig,
	client *mongo.Client,
	wc WatermarkChecker,
	idGen *event.IDGenerator,
	appendFn func(context.Context, *event.ChangeEvent) error,
) *MongoSnapshot {
	return &MongoSnapshot{
		cfg:      cfg,
		client:   client,
		wc:       wc,
		idGen:    idGen,
		appendFn: appendFn,
	}
}

// SetFindFn replaces the default MongoDB Find with a test stub.
// The stub signature matches the internal findFn field.
func (s *MongoSnapshot) SetFindFn(fn func(ctx context.Context, coll string, filter any, opts ...any) ([]bson.Raw, error)) {
	s.findFn = fn
}

// SetSnapshotLSN sets the snapshot LSN directly (used in unit tests to skip
// the real cluster-time capture that requires a live MongoDB connection).
func (s *MongoSnapshot) SetSnapshotLSN(lsn uint64) {
	s.snapshotLSN = lsn
}

// SetBatchSize overrides the keyset window size. Values <= 0 restore the default.
func (s *MongoSnapshot) SetBatchSize(n int) {
	s.batchSize = n
}

// SetProgressStore enables durable last-`_id` resume across mid-snapshot crashes.
// Keys are distinct from change-stream resume tokens. Passing nil disables resume.
func (s *MongoSnapshot) SetProgressStore(store checkpoint.CheckpointStore) {
	s.progressStore = store
}

func (s *MongoSnapshot) effectiveBatchSize() int {
	if s.batchSize > 0 {
		return s.batchSize
	}
	return defaultSnapshotBatchSize
}

// progressKey is the checkpoint key for snapshot keyset resume. It must not
// collide with change-stream resume tokens (sourceID:coll).
func (s *MongoSnapshot) progressKey(collName string) string {
	return s.cfg.SourceID + ":" + collName + ":snapshot"
}

// Run executes the snapshot for all collections. It returns context.Canceled
// when the context is cancelled and nil on successful completion.
//
// For each collection:
//  1. Documents are fetched in `_id` keyset windows via findFn (or real Find).
//  2. Each document is converted to OpRead via the MongoDB normalizer.
//  3. WatermarkChecker.ShouldEmit gates each row — rows returning false are skipped.
//  4. Passing rows are forwarded to appendFn.
//  5. After each window, the last processed `_id` is persisted (when a progress
//     store is configured) so a crash can resume.
//  6. After all rows, an OpControl "snapshot_complete" event is appended and
//     snapshot progress is cleared.
func (s *MongoSnapshot) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Capture snapshot LSN from cluster time if not already set by tests.
	if s.snapshotLSN == 0 && s.client != nil {
		lsn, err := s.captureSnapshotLSN(ctx)
		if err != nil {
			// Non-fatal: if we can't get cluster time, use 0 (watermark check
			// will pass everything, matching Postgres fallback behaviour).
			lsn = 0
		}
		s.snapshotLSN = lsn
	}

	for _, coll := range s.cfg.Collections {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.snapshotCollection(ctx, coll); err != nil {
			return err
		}
	}
	return nil
}

func (s *MongoSnapshot) snapshotCollection(ctx context.Context, collName string) error {
	// Build the O(1) watermark index for this collection before scanning any
	// documents, if the checker supports it. Non-fatal on error: ShouldEmit
	// transparently falls back to the per-row scan path.
	if idx, ok := s.wc.(IndexedWatermarkChecker); ok {
		if startErr := idx.StartTable(ctx, collName, s.snapshotLSN); startErr != nil {
			slog.Warn("mongodb snapshot: watermark index build failed, falling back to per-row scan",
				"error", startErr, "collection", collName)
		} else {
			defer idx.FinishTable(collName)
		}
	}

	batchSize := s.effectiveBatchSize()
	afterID, err := s.loadProgress(ctx, collName)
	if err != nil {
		return fmt.Errorf("mongodb snapshot: load progress %s: %w", collName, err)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		docs, fetchErr := s.fetchPage(ctx, collName, afterID, batchSize)
		if fetchErr != nil {
			return fmt.Errorf("mongodb snapshot: fetch %s: %w", collName, fetchErr)
		}
		if len(docs) == 0 {
			break
		}

		var lastID any
		for _, raw := range docs {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			id, idErr := extractDocID(raw)
			if idErr != nil {
				// Skip unparseable documents with a warning rather than aborting.
				continue
			}
			lastID = id

			// Normalize the raw BSON document into a ChangeEvent.
			// NormalizeChangeEvent expects a Change Stream event shape, but for
			// snapshot rows we build a synthetic change stream doc wrapping the raw doc.
			ev, normErr := s.normalizeSnapshotDoc(raw, collName)
			if normErr != nil {
				// Skip unparseable documents with a warning rather than aborting.
				continue
			}
			// Override operation to OpRead (snapshot row, not a live change).
			ev.Operation = event.OpRead

			// Watermark check: skip if a newer WAL event already exists in the log.
			emit, wcErr := s.wc.ShouldEmit(ctx, collName, ev.Key, s.snapshotLSN)
			if wcErr != nil {
				return fmt.Errorf("mongodb snapshot: watermark check %s: %w", collName, wcErr)
			}
			if !emit {
				continue
			}

			if appendErr := s.appendFn(ctx, ev); appendErr != nil {
				return fmt.Errorf("mongodb snapshot: append %s: %w", collName, appendErr)
			}
		}

		// Persist last scanned `_id` after the window is fully processed so a
		// mid-snapshot crash resumes past this window (at-least-once; EventLog
		// dedups re-emits by IdempotencyKey).
		if lastID != nil {
			afterID = lastID
			if saveErr := s.saveProgress(ctx, collName, lastID); saveErr != nil {
				return fmt.Errorf("mongodb snapshot: save progress %s: %w", collName, saveErr)
			}
		}

		// Short page → last window.
		if len(docs) < batchSize {
			break
		}
	}

	// Append OpControl "snapshot_complete" event after all rows for this collection.
	controlEv := &event.ChangeEvent{
		ID:             s.idGen.New(),
		IdempotencyKey: fmt.Sprintf("%s:%s:%s:%d", s.cfg.SourceID, collName, "snapshot_complete", time.Now().UnixNano()),
		Source:         s.cfg.SourceID,
		Operation:      event.OpControl,
		Database:       s.cfg.Database,
		Table:          collName,
		Metadata: map[string]any{
			"event":    "snapshot_complete",
			"snapshot": true,
		},
	}
	if err := s.appendFn(ctx, controlEv); err != nil {
		return err
	}
	return s.clearProgress(ctx, collName)
}

// normalizeSnapshotDoc converts a raw BSON document (not a change stream event)
// into a ChangeEvent suitable for snapshot emission.
//
// Snapshot documents are plain collection documents (not change stream wrappers),
// so we build a synthetic change stream shape and call NormalizeChangeEvent.
// The resulting event's Operation is overridden to OpRead by the caller.
func (s *MongoSnapshot) normalizeSnapshotDoc(raw bson.Raw, collName string) (*event.ChangeEvent, error) {
	// Build a synthetic change stream event document around the raw doc.
	// This lets us reuse the normalizer without duplicating logic.
	//
	// Required fields in a change stream event:
	//   operationType: "insert"   (will be overridden to OpRead by caller)
	//   ns.db: database
	//   ns.coll: collection
	//   documentKey: {_id: ...}
	//   fullDocument: <the raw doc>
	//   _id: synthetic resume token
	//   clusterTime: synthetic timestamp

	// Extract _id from the raw document for the documentKey.
	idVal, err := raw.LookupErr("_id")
	if err != nil {
		return nil, fmt.Errorf("snapshot doc missing _id: %w", err)
	}

	docKey, _ := bson.Marshal(bson.D{{Key: "_id", Value: idVal}})

	syntheticToken, _ := bson.Marshal(bson.D{{Key: "_data", Value: "snapshot"}})

	// Build the ns sub-document.
	nsDoc, _ := bson.Marshal(bson.D{
		{Key: "db", Value: s.cfg.Database},
		{Key: "coll", Value: collName},
	})

	// Build the synthetic change stream wrapper.
	wrapper, _ := bson.Marshal(bson.D{
		{Key: "_id", Value: bson.Raw(syntheticToken)},
		{Key: "operationType", Value: "insert"},
		{Key: "ns", Value: bson.Raw(nsDoc)},
		{Key: "documentKey", Value: bson.Raw(docKey)},
		{Key: "fullDocument", Value: raw},
		{Key: "clusterTime", Value: bson.Timestamp{T: uint32(s.snapshotLSN >> 32), I: uint32(s.snapshotLSN)}},
	})

	ev, normErr := mongoparser.NormalizeChangeEvent(bson.Raw(wrapper), s.cfg.SourceID, s.idGen)
	if normErr != nil {
		return nil, normErr
	}
	return ev, nil
}

// fetchPage retrieves one keyset window of documents sorted by `_id`, starting
// after afterID (nil = from the beginning). Never uses OFFSET.
//
// In unit tests, findFn is injected via SetFindFn. In production, a real
// mongo.Collection.Find call is made with Limit(batchSize).
func (s *MongoSnapshot) fetchPage(ctx context.Context, collName string, afterID any, batchSize int) ([]bson.Raw, error) {
	filter := bson.D{}
	if afterID != nil {
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}}}
	}

	if s.findFn != nil {
		return s.findFn(ctx, collName, filter, int64(batchSize))
	}

	if s.client == nil {
		return nil, fmt.Errorf("mongodb snapshot: no client or findFn configured for collection %s", collName)
	}

	db := s.client.Database(s.cfg.Database)
	coll := db.Collection(collName)

	opts := mongoopts.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetLimit(int64(batchSize))
	cursor, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	// Cap capacity to one window so peak buffering stays O(batchSize).
	results := make([]bson.Raw, 0, batchSize)
	for cursor.Next(ctx) {
		var raw bson.Raw
		if err := cursor.Decode(&raw); err != nil {
			continue
		}
		results = append(results, raw)
	}
	return results, cursor.Err()
}

func extractDocID(raw bson.Raw) (any, error) {
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

func (s *MongoSnapshot) loadProgress(ctx context.Context, collName string) (any, error) {
	if s.progressStore == nil {
		return nil, nil
	}
	encoded, err := s.progressStore.Load(ctx, s.progressKey(collName))
	if err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, nil
	}
	return decodeSnapshotCursor(encoded)
}

func (s *MongoSnapshot) saveProgress(ctx context.Context, collName string, afterID any) error {
	if s.progressStore == nil || afterID == nil {
		return nil
	}
	encoded, err := encodeSnapshotCursor(afterID)
	if err != nil {
		return err
	}
	// Never persist an empty progress value — empty means "start from head".
	if encoded == "" {
		return nil
	}
	return s.progressStore.Save(ctx, s.progressKey(collName), encoded)
}

func (s *MongoSnapshot) clearProgress(ctx context.Context, collName string) error {
	if s.progressStore == nil {
		return nil
	}
	// CheckpointStore has no Delete; empty string means no resume cursor.
	return s.progressStore.Save(ctx, s.progressKey(collName), "")
}

// encodeSnapshotCursor stores afterID as Extended JSON of {"_id": <value>}.
func encodeSnapshotCursor(afterID any) (string, error) {
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: afterID}})
	if err != nil {
		return "", err
	}
	return bson.Raw(raw).String(), nil
}

func decodeSnapshotCursor(s string) (any, error) {
	var raw bson.Raw
	if err := bson.UnmarshalExtJSON([]byte(s), false, &raw); err != nil {
		return nil, fmt.Errorf("parse snapshot cursor (len=%d): %w", len(s), err)
	}
	return extractDocID(raw)
}

// captureSnapshotLSN gets the current cluster time from MongoDB and encodes it
// as uint64(clusterTime.T)<<32 | uint64(clusterTime.I). This value is used as
// the snapshotLSN for watermark comparisons during snapshot.
func (s *MongoSnapshot) captureSnapshotLSN(ctx context.Context) (uint64, error) {
	var result bson.Raw
	err := s.client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&result)
	if err != nil {
		return 0, fmt.Errorf("mongodb snapshot: hello command: %w", err)
	}

	clusterTimeVal, err := result.LookupErr("$clusterTime")
	if err != nil {
		// Standalone MongoDB or older version — no cluster time.
		return 0, nil
	}

	var clusterTimeDoc struct {
		ClusterTime bson.Timestamp `bson:"clusterTime"`
	}
	ctDoc, ok := clusterTimeVal.DocumentOK()
	if !ok {
		return 0, nil
	}
	if err := bson.Unmarshal(ctDoc, &clusterTimeDoc); err != nil {
		return 0, nil
	}

	t := clusterTimeDoc.ClusterTime
	return uint64(t.T)<<32 | uint64(t.I), nil
}
