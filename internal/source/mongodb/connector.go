// Package mongodb implements the MongoDBConnector: a CDC source that consumes
// MongoDB Change Streams, persists resume tokens, and emits ChangeEvents.
//
// Critical invariant (CHK-01): the resume token is NEVER saved to the
// checkpoint store until after the corresponding event(s) are durably
// appended to the EventLog. This guarantees that on restart the source
// re-delivers any event that was not durably committed.
//
// CHK-01 batch granularity: change-stream events are appended to the
// EventLog in batches (see consumeStream), and the resume token is saved
// once per batch — the token of the LAST event in the batch — rather than
// once per event. A crash mid-batch re-opens the stream from the PREVIOUS
// batch's token and re-sends the whole in-flight batch (up to
// maxChangeStreamBatchSize events) on restart, instead of just the last
// event. This is safe: EventLog.Append/AppendBatch dedups by
// IdempotencyKey (LOG-03), so the re-sent events collapse into no-ops. Only
// the size of the replay window on restart changes, not durability.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	mongoparser "github.com/olucasandrade/kaptanto/internal/parser/mongodb"
)

const (
	defaultInitialBackoff = 2 * time.Second
	defaultMaxBackoff     = 60 * time.Second

	// maxChangeStreamBatchSize caps the number of events drained via TryNext
	// into a single AppendBatch call (see consumeStream). Mirrors the
	// Postgres WAL path's per-commit batch cap.
	maxChangeStreamBatchSize = 256
)

// ChangeStreamIter is the injectable interface for a MongoDB change stream
// cursor. The real implementation wraps *mongo.ChangeStream; tests inject
// a fake.
type ChangeStreamIter interface {
	Next(ctx context.Context) bool
	// TryNext is a non-blocking advance: it reports whether a document is
	// already available in the driver's internal buffer without waiting for
	// a new one to arrive from the server. Used to greedily drain already
	// buffered events into a batch (see consumeStream). *mongo.ChangeStream
	// provides this natively.
	TryNext(ctx context.Context) bool
	Decode(v any) error
	ResumeToken() bson.Raw
	Err() error
	Close(ctx context.Context) error
}

// WatchFn is the function type injected for opening a change stream on a
// collection. In production this wraps mongo.Collection.Watch; in tests a
// fake is supplied.
type WatchFn func(ctx context.Context, collection string, resumeToken bson.Raw) (ChangeStreamIter, error)

// Config holds all parameters for the MongoDBConnector.
type Config struct {
	// URI is the MongoDB connection string, e.g. "mongodb://localhost:27017".
	URI string

	// Database is the target database name. Required; New/NewWithEventLog
	// return an error if blank.
	Database string

	// Collections lists collection names to watch. Must be non-empty.
	Collections []string

	// SourceID is the stable identifier for checkpoint keying. Defaults to
	// "mongo_default" when blank.
	SourceID string
}

// ApplyDefaults fills zero-value Config fields with their defaults.
// It does NOT validate required fields — call after Apply to check.
func (c *Config) ApplyDefaults() {
	if c.SourceID == "" {
		c.SourceID = "mongo_default"
	}
}

// validate checks that required fields are set.
func (c *Config) validate() error {
	if c.Database == "" {
		return errors.New("mongodb: database is required")
	}
	return nil
}

// MongoDBConnector consumes MongoDB Change Streams and emits ChangeEvents.
// It persists resume tokens to a CheckpointStore so it can survive restarts.
//
// CHK-01 invariant: when an EventLog is configured, the resume token is saved
// only after the durable write (Append or AppendBatch) succeeds. A crash
// between the durable write and the token Save means the source will
// re-deliver the not-yet-checkpointed event(s) on restart; the EventLog's
// idempotency key deduplicates the duplicate delivery. See the package doc
// comment for the batch-granularity nuance introduced by consumeStream.
type MongoDBConnector struct {
	cfg           Config
	store         checkpoint.CheckpointStore
	idGen         *event.IDGenerator
	eventLog      eventlog.EventLog // nil if no durable log
	events        chan *event.ChangeEvent
	needsSnapshot bool
	mu            sync.Mutex // guards needsSnapshot

	// resumeToken is loaded from the checkpoint store on construction and
	// passed as the ResumeAfter option when opening each change stream.
	resumeToken bson.Raw

	// watchFn opens a change stream cursor for the given collection. It is
	// set to realWatchFn in New/NewWithEventLog and replaced by tests.
	watchFn WatchFn

	// client is the underlying MongoDB client (nil when watchFn is injected
	// by tests, so we only connect when a real URI is provided).
	client *mongo.Client

	// batchStats tracks flush/event counts across all collection goroutines,
	// exposed via BatchStats for tests/observability that need to confirm
	// batching is actually occurring (as opposed to always flushing size-1
	// batches).
	batchesFlushed atomic.Uint64
	eventsFlushed  atomic.Uint64
}

// New creates a MongoDBConnector without a durable EventLog. Delegates to
// NewWithEventLog(cfg, store, idGen, nil).
func New(cfg Config, store checkpoint.CheckpointStore, idGen *event.IDGenerator) (*MongoDBConnector, error) {
	return NewWithEventLog(cfg, store, idGen, nil)
}

// NewWithEventLog creates a MongoDBConnector with a durable EventLog. When el
// is non-nil, AppendAndQueue calls el.Append before saving the resume token
// (CHK-01 ordering).
func NewWithEventLog(cfg Config, store checkpoint.CheckpointStore, idGen *event.IDGenerator, el eventlog.EventLog) (*MongoDBConnector, error) {
	cfg.ApplyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	c := &MongoDBConnector{
		cfg:      cfg,
		store:    store,
		idGen:    idGen,
		eventLog: el,
		events:   make(chan *event.ChangeEvent, 1024),
	}

	// Load the last persisted resume token from the checkpoint store.
	tokenStr, err := store.Load(context.Background(), cfg.SourceID)
	if err != nil {
		return nil, fmt.Errorf("mongodb: load checkpoint: %w", err)
	}
	if tokenStr != "" {
		// The token is stored as a JSON/BSON hex string representation.
		// Reconstruct as bson.Raw from the stored string.
		raw, parseErr := tokenFromString(tokenStr)
		if parseErr != nil {
			slog.Warn("mongodb: could not parse stored resume token, starting from head",
				"err", parseErr, "stored", tokenStr)
		} else {
			c.resumeToken = raw
		}
	}

	// The real watchFn is set separately (after the client is connected).
	// For production use, callers must call Run which lazily connects.
	// For test injection, use NewWithWatchFn.
	return c, nil
}

// NewWithWatchFn creates a MongoDBConnector with a custom WatchFn (for
// testing). The EventLog may be nil. No real MongoDB connection is made.
func NewWithWatchFn(
	cfg Config,
	store checkpoint.CheckpointStore,
	idGen *event.IDGenerator,
	el eventlog.EventLog,
	watchFn WatchFn,
) (*MongoDBConnector, error) {
	c, err := NewWithEventLog(cfg, store, idGen, el)
	if err != nil {
		return nil, err
	}
	c.watchFn = watchFn
	return c, nil
}

// HasEventLog reports whether a durable EventLog was provided. Exposed for
// testing (mirrors PostgresConnector.EventLog() usage).
func (c *MongoDBConnector) HasEventLog() bool {
	return c.eventLog != nil
}

// NeedsSnapshot reports whether the connector detected an InvalidResumeToken
// error, which means the caller must trigger a full re-snapshot.
func (c *MongoDBConnector) NeedsSnapshot() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.needsSnapshot
}

// BatchStats returns the cumulative number of AppendBatch flushes and events
// flushed across all collection goroutines. Exposed for tests/observability
// to confirm that batching is actually happening (events > batches implies
// at least one batch held more than one event) rather than always degrading
// to size-1 batches.
func (c *MongoDBConnector) BatchStats() (batches, events uint64) {
	return c.batchesFlushed.Load(), c.eventsFlushed.Load()
}

// Events returns the read-only channel on which ChangeEvents are emitted.
// Callers should range over this channel concurrently with Run.
func (c *MongoDBConnector) Events() <-chan *event.ChangeEvent {
	return c.events
}

// AppendAndQueue durably appends ev to the EventLog (if configured) and
// forwards ev to the events channel. If el.Append fails, the error is
// returned and the resume token is NOT saved (CHK-01 invariant).
//
// token is the change stream resume token associated with ev and is saved
// to the checkpoint store after a successful Append.
//
// This single-event path remains available for callers that don't batch
// (e.g. direct unit-test exercise of CHK-01); consumeStream uses
// AppendAndQueueBatch on the hot path.
func (c *MongoDBConnector) AppendAndQueue(ctx context.Context, ev *event.ChangeEvent, token bson.Raw) error {
	if c.eventLog != nil {
		if _, err := c.eventLog.Append(ev); err != nil {
			return fmt.Errorf("mongodb: eventlog append: %w", err)
		}
	}

	// CHK-01: save token only after durable write.
	tokenStr := tokenToString(token)
	if err := c.store.Save(ctx, c.cfg.SourceID, tokenStr); err != nil {
		return fmt.Errorf("mongodb: save checkpoint: %w", err)
	}

	// Forward to channel: drain-or-drop (Router reads from EventLog, not this
	// channel). Drop is safe because the event is already durable.
	select {
	case c.events <- ev:
	default:
	}

	c.batchesFlushed.Add(1)
	c.eventsFlushed.Add(1)
	return nil
}

// AppendAndQueueBatch durably appends evs to the EventLog in a single
// AppendBatch call (if configured) and forwards each event to the events
// channel individually (drain-or-drop, same semantics as AppendAndQueue —
// batching only changes how many events share one durable write and one
// checkpoint save, not the per-event forwarding contract).
//
// lastToken must be the resume token of the LAST event in evs. It is the
// only token saved to the checkpoint store for this batch (CHK-01 batch
// granularity — see the package doc comment). If AppendBatch fails, none of
// the events are forwarded and the token is NOT saved.
func (c *MongoDBConnector) AppendAndQueueBatch(ctx context.Context, evs []*event.ChangeEvent, lastToken bson.Raw) error {
	if len(evs) == 0 {
		return nil
	}

	if c.eventLog != nil {
		if _, err := c.eventLog.AppendBatch(evs); err != nil {
			return fmt.Errorf("mongodb: eventlog append batch: %w", err)
		}
	}

	// CHK-01: save the LAST event's token only after the durable write
	// succeeds. Saved once per batch, not once per event.
	if err := c.saveResumeToken(ctx, lastToken); err != nil {
		return err
	}

	// Forward to channel: drain-or-drop per event, looped rather than bulk
	// sent, preserving the existing per-event drop semantics.
	for _, ev := range evs {
		select {
		case c.events <- ev:
		default:
		}
	}

	c.batchesFlushed.Add(1)
	c.eventsFlushed.Add(uint64(len(evs)))
	return nil
}

// saveResumeToken persists the given resume token to the checkpoint store.
// It is used both by the batch path and when advancing past documents that
// could not be decoded or normalized.
func (c *MongoDBConnector) saveResumeToken(ctx context.Context, token bson.Raw) error {
	tokenStr := tokenToString(token)
	if err := c.store.Save(ctx, c.cfg.SourceID, tokenStr); err != nil {
		return fmt.Errorf("mongodb: save checkpoint: %w", err)
	}
	return nil
}

// Run starts the outer reconnect loop for all configured collections. For each
// collection, a goroutine is started that consumes the change stream. Run
// returns when ctx is cancelled (returns context.Canceled) or when an
// InvalidResumeToken error is detected (returns nil with NeedsSnapshot=true).
//
// If a real MongoDB URI is configured and no WatchFn was injected, Run lazily
// creates the real client and watch function.
func (c *MongoDBConnector) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Lazily set up the real watchFn if none was injected.
	if c.watchFn == nil {
		if err := c.connectReal(ctx); err != nil {
			return err
		}
		defer func() {
			if c.client != nil {
				_ = c.client.Disconnect(context.Background())
			}
		}()
	}

	// errCh collects the first error (or nil) from any collection goroutine.
	type result struct {
		needsSnapshot bool
		err           error
	}
	resultCh := make(chan result, len(c.cfg.Collections))

	for _, coll := range c.cfg.Collections {
		go func(collName string) {
			ns, err := c.runCollection(ctx, collName)
			resultCh <- result{needsSnapshot: ns, err: err}
		}(coll)
	}

	// Wait for all goroutines to finish.
	var firstErr error
	var anyNeedsSnapshot bool
	for range c.cfg.Collections {
		r := <-resultCh
		if r.needsSnapshot {
			anyNeedsSnapshot = true
		}
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
	}

	if anyNeedsSnapshot {
		c.mu.Lock()
		c.needsSnapshot = true
		c.mu.Unlock()
		return nil
	}

	if firstErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return firstErr
	}

	return ctx.Err()
}

// runCollection runs the change stream loop for a single collection. It
// returns (needsSnapshot=true, nil) on InvalidResumeToken, (false, ctx.Err())
// on cancellation, or (false, err) on other fatal errors.
func (c *MongoDBConnector) runCollection(ctx context.Context, collName string) (needsSnapshot bool, err error) {
	backoff := defaultInitialBackoff

	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		iter, openErr := c.watchFn(ctx, collName, c.resumeToken)
		if openErr != nil {
			if isInvalidResumeToken(openErr) {
				return true, nil
			}
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			slog.Warn("mongodb: open change stream failed, retrying",
				"collection", collName, "error", openErr, "backoff", backoff)
			select {
			case <-time.After(backoff):
				backoff = nextBackoff(backoff)
				continue
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}

		// Reset backoff on successful open.
		backoff = defaultInitialBackoff

		loopNeedsSnapshot, loopErr := c.consumeStream(ctx, collName, iter)
		_ = iter.Close(ctx)

		if loopNeedsSnapshot {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if loopErr != nil {
			slog.Warn("mongodb: change stream error, retrying",
				"collection", collName, "error", loopErr, "backoff", backoff)
			select {
			case <-time.After(backoff):
				backoff = nextBackoff(backoff)
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
	}
}

// decodeNext decodes the document iter is currently positioned on, captures
// its resume token, and normalizes it into a ChangeEvent. On decode or
// normalize failure it logs a warning and returns ok=false so the caller can
// skip this document without aborting the surrounding batch — the same
// skip-and-continue behavior as before batching was introduced.
//
// The resume token is read via iter.ResumeToken() immediately after Decode,
// matching the token to the document that was just consumed.
func (c *MongoDBConnector) decodeNext(collName string, iter ChangeStreamIter) (ev *event.ChangeEvent, token bson.Raw, ok bool) {
	var rawDoc bson.Raw
	decErr := iter.Decode(&rawDoc)

	// Capture the resume token that corresponds to the document the iterator
	// just consumed, regardless of whether decoding or normalization succeeds.
	// This lets the caller advance the checkpoint past documents that cannot be
	// parsed, preventing a single bad document from stalling the change stream.
	token = iter.ResumeToken()

	if decErr != nil {
		slog.Warn("mongodb: decode change stream doc", "collection", collName, "error", decErr)
		return nil, token, false
	}

	ev, normErr := mongoparser.NormalizeChangeEvent(rawDoc, c.cfg.SourceID, c.idGen)
	if normErr != nil {
		slog.Warn("mongodb: normalize change event", "collection", collName, "error", normErr)
		return nil, token, false
	}
	return ev, token, true
}

// consumeStream iterates over events from iter until iter is exhausted, the
// context is cancelled, or an InvalidResumeToken error occurs.
//
// Batching (perf): each outer iteration blocks on Next for the first event,
// then greedily drains any further ALREADY-buffered events via the
// non-blocking TryNext (capped at maxChangeStreamBatchSize) before flushing
// the whole batch through a single AppendAndQueueBatch call. This amortizes
// the EventLog's per-Append fsync (LOG-01) across many events instead of
// paying it once per event — the same fix already applied to the Postgres
// WAL path (see postgres.receiveLoop's flushWALBuf).
//
// TryNext still issues a network round-trip ("getMore") when its internal
// buffer is empty, so the drain loop is only entered after a successful
// blocking Next, and it flushes immediately on the first empty TryNext
// rather than waiting or retrying for more. At low event rates this
// degenerates to exactly today's per-event batches of size 1, with no added
// latency and no extra getMore traffic.
//
// See the package doc comment for the CHK-01 batch-granularity change this
// introduces (resume token saved once per batch, not once per event).
func (c *MongoDBConnector) consumeStream(ctx context.Context, collName string, iter ChangeStreamIter) (needsSnapshot bool, err error) {
	for iter.Next(ctx) {
		batch := make([]*event.ChangeEvent, 0, 1)
		var lastToken bson.Raw

		if ev, token, ok := c.decodeNext(collName, iter); ok {
			batch = append(batch, ev)
			lastToken = token
		} else if token != nil {
			// Document was consumed but could not be decoded/normalized.
			// Remember its token so we can advance the checkpoint past it.
			lastToken = token
		}

		for len(batch) < maxChangeStreamBatchSize && iter.TryNext(ctx) {
			ev, token, ok := c.decodeNext(collName, iter)
			if !ok {
				if token != nil {
					lastToken = token
				}
				continue
			}
			batch = append(batch, ev)
			lastToken = token
		}

		if len(batch) > 0 {
			if aqErr := c.AppendAndQueueBatch(ctx, batch, lastToken); aqErr != nil {
				return false, aqErr
			}
		} else if lastToken != nil {
			// All documents in this batch failed parsing; still advance the
			// checkpoint so a persistently bad document cannot stall progress.
			if saveErr := c.saveResumeToken(ctx, lastToken); saveErr != nil {
				return false, saveErr
			}
		}
	}

	if iterErr := iter.Err(); iterErr != nil {
		if isInvalidResumeToken(iterErr) {
			return true, nil
		}
		return false, iterErr
	}
	return false, nil
}

// connectReal creates the real MongoDB client and sets up the watchFn.
func (c *MongoDBConnector) connectReal(ctx context.Context) error {
	opts := mongoopts.Client().ApplyURI(c.cfg.URI)
	client, err := mongo.Connect(opts)
	if err != nil {
		return fmt.Errorf("mongodb: connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return fmt.Errorf("mongodb: ping: %w", err)
	}
	c.client = client
	db := client.Database(c.cfg.Database)
	c.watchFn = func(watchCtx context.Context, collName string, resumeToken bson.Raw) (ChangeStreamIter, error) {
		coll := db.Collection(collName)
		var watchOpts *mongoopts.ChangeStreamOptionsBuilder
		if len(resumeToken) > 0 {
			watchOpts = mongoopts.ChangeStream().SetResumeAfter(resumeToken)
		} else {
			watchOpts = mongoopts.ChangeStream()
		}
		watchOpts = watchOpts.SetFullDocument(mongoopts.UpdateLookup)
		cs, err := coll.Watch(watchCtx, mongo.Pipeline{}, watchOpts)
		if err != nil {
			return nil, err
		}
		return cs, nil
	}
	return nil
}

// isInvalidResumeToken returns true if err is a MongoDB CommandError with
// code 260 (InvalidResumeToken) or an error message containing that string.
func isInvalidResumeToken(err error) bool {
	if err == nil {
		return false
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == 260 {
		return true
	}
	return strings.Contains(err.Error(), "InvalidResumeToken")
}

// tokenToString encodes a bson.Raw resume token as a hex string for storage
// in the checkpoint store.
func tokenToString(token bson.Raw) string {
	if len(token) == 0 {
		return ""
	}
	return token.String()
}

// tokenFromString parses a stored token string back to bson.Raw. The string
// is expected to be the output of bson.Raw.String() (extended JSON map).
func tokenFromString(s string) (bson.Raw, error) {
	if s == "" {
		return nil, nil
	}
	// bson.Raw.String() returns extended JSON; parse it back via bson.UnmarshalExtJSON
	var raw bson.Raw
	if err := bson.UnmarshalExtJSON([]byte(s), false, &raw); err != nil {
		// Fallback: try treating as raw BSON hex
		return nil, fmt.Errorf("parse resume token %q: %w", s, err)
	}
	return raw, nil
}

// nextBackoff doubles backoff, capped at defaultMaxBackoff.
func nextBackoff(b time.Duration) time.Duration {
	b *= 2
	if b > defaultMaxBackoff {
		return defaultMaxBackoff
	}
	return b
}
