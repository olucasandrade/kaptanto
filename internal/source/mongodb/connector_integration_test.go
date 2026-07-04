package mongodb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// These tests exercise the real MongoDB Change Stream path. They require a
// MongoDB running as a replica set (Change Streams are unavailable on a
// standalone server). Set MONGO_TEST_URI to enable them, e.g.:
//
//	MONGO_TEST_URI="mongodb://localhost:27017/?replicaSet=rs0" go test ./internal/source/mongodb/...
//
// The integration workflow (.github/workflows/integration.yml) provisions a
// single-node replica set and exports MONGO_TEST_URI.

func mongoTestURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("set MONGO_TEST_URI (replica-set MongoDB) to run MongoDB integration tests")
	}
	return uri
}

// connectMongo returns a real client and registers cleanup.
func connectMongo(t *testing.T, uri string) *mongo.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cli, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err, "connect to MongoDB")
	require.NoError(t, cli.Ping(ctx, nil), "ping MongoDB")
	t.Cleanup(func() {
		_ = cli.Disconnect(context.Background())
	})
	return cli
}

// readEvent waits for the next ChangeEvent or fails on timeout.
func readEvent(t *testing.T, ch <-chan *event.ChangeEvent, within time.Duration) *event.ChangeEvent {
	t.Helper()
	select {
	case ev := <-ch:
		require.NotNil(t, ev)
		return ev
	case <-time.After(within):
		t.Fatalf("timed out after %s waiting for ChangeEvent", within)
		return nil
	}
}

// TestMongoIntegration_ChangeStream_CRUD verifies that inserts, updates and
// deletes against a watched collection surface as ordered ChangeEvents with
// the correct operations.
func TestMongoIntegration_ChangeStream_CRUD(t *testing.T) {
	uri := mongoTestURI(t)
	cli := connectMongo(t, uri)

	// Unique collection per run to avoid cross-test interference.
	dbName := "kaptanto_it"
	collName := "events_" + time.Now().Format("150405.000000")
	coll := cli.Database(dbName).Collection(collName)
	t.Cleanup(func() {
		_ = coll.Drop(context.Background())
	})

	conn, err := mongodb.New(mongodb.Config{
		URI:         uri,
		Database:    dbName,
		Collections: []string{collName},
		SourceID:    "it_" + collName,
	}, newFakeStore(), event.NewIDGenerator())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- conn.Run(ctx) }()

	// Give the change stream a moment to open before producing writes, so the
	// resume position starts ahead of our operations.
	time.Sleep(2 * time.Second)

	docID := bson.NewObjectID()
	_, err = coll.InsertOne(ctx, bson.M{"_id": docID, "status": "new"})
	require.NoError(t, err)
	_, err = coll.UpdateOne(ctx, bson.M{"_id": docID}, bson.M{"$set": bson.M{"status": "done"}})
	require.NoError(t, err)
	_, err = coll.DeleteOne(ctx, bson.M{"_id": docID})
	require.NoError(t, err)

	ch := conn.Events()
	ins := readEvent(t, ch, 15*time.Second)
	upd := readEvent(t, ch, 15*time.Second)
	del := readEvent(t, ch, 15*time.Second)

	require.Equal(t, event.OpInsert, ins.Operation, "first event should be insert")
	require.Equal(t, event.OpUpdate, upd.Operation, "second event should be update")
	require.Equal(t, event.OpDelete, del.Operation, "third event should be delete")

	// All three events concern the same document key, in order.
	require.JSONEq(t, string(ins.Key), string(upd.Key), "insert/update share the document key")
	require.JSONEq(t, string(upd.Key), string(del.Key), "update/delete share the document key")

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("connector did not stop after context cancellation")
	}
}

// TestMongoIntegration_BatchedAppends_UnderLoad drives a burst of inserts
// against the connector and asserts that batching actually occurs: the
// number of AppendBatch/AppendAndQueue flushes (BatchStats' batches) must be
// smaller than the number of events flushed, proving at least one flush
// combined multiple change-stream events instead of degrading to one flush
// per event.
//
// This can only be asserted against a live server: the effect depends on
// the driver's internal getMore/buffering behavior under real network and
// server timing, which the in-process fakeIter used by the unit tests above
// cannot faithfully reproduce.
func TestMongoIntegration_BatchedAppends_UnderLoad(t *testing.T) {
	uri := mongoTestURI(t)
	cli := connectMongo(t, uri)

	dbName := "kaptanto_it"
	collName := "batch_load_" + time.Now().Format("150405.000000")
	coll := cli.Database(dbName).Collection(collName)
	t.Cleanup(func() {
		_ = coll.Drop(context.Background())
	})

	conn, err := mongodb.New(mongodb.Config{
		URI:         uri,
		Database:    dbName,
		Collections: []string{collName},
		SourceID:    "it_batch_" + collName,
	}, newFakeStore(), event.NewIDGenerator())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- conn.Run(ctx) }()

	// Give the change stream a moment to open before producing writes.
	time.Sleep(2 * time.Second)

	const n = 500
	docs := make([]interface{}, n)
	for i := 0; i < n; i++ {
		docs[i] = bson.M{"seq": i}
	}
	_, err = coll.InsertMany(ctx, docs)
	require.NoError(t, err, "burst insert of %d documents", n)

	// Poll BatchStats (atomic counters on the connector, safe to read
	// concurrently with the Run goroutine) until every inserted document has
	// been flushed, or time out. The events channel has a 1024-entry buffer,
	// comfortably larger than n, so no concurrent drain is required to avoid
	// drops here.
	deadline := time.Now().Add(30 * time.Second)
	var batches, events uint64
	for time.Now().Before(deadline) {
		batches, events = conn.BatchStats()
		if events >= uint64(n) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	require.Equal(t, uint64(n), events, "all %d inserted documents must be flushed as change events", n)
	t.Logf("batch stats: %d batches for %d events (avg batch size %.1f)",
		batches, events, float64(events)/float64(batches))
	assert.Less(t, batches, events,
		"sustained insert load must produce at least one multi-event batch (batches < events); "+
			"if this fails, batching regressed to one AppendBatch call per event")

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("connector did not stop after context cancellation")
	}
}

// extractSeq pulls the "seq" field out of ev.After. seq is stored as a
// string (not an int) so it survives the canonical Extended JSON encoding
// NormalizeChangeEvent applies without needing to unwrap a $numberInt/
// $numberLong wrapper. Returns ok=false for events with no After (deletes).
func extractSeq(ev *event.ChangeEvent) (string, bool) {
	if len(ev.After) == 0 {
		return "", false
	}
	var doc struct {
		Seq string `json:"seq"`
	}
	if err := json.Unmarshal(ev.After, &doc); err != nil || doc.Seq == "" {
		return "", false
	}
	return doc.Seq, true
}

// TestMongoIntegration_CrashResume_NoGaps simulates a crash mid-stream: it
// cancels a running connector partway through a burst of inserts (before
// all of them are necessarily flushed), then starts a second connector
// instance resuming from the same checkpoint store. It asserts every
// inserted document is eventually observed by connector A and/or B
// combined.
//
// This is the integration-level proof for the CHK-01 batch-granularity
// change documented on MongoDBConnector: the resume token is saved once per
// BATCH (the last event's token), not once per event, so a crash mid-batch
// re-opens the stream from the PREVIOUS batch's token and connector B may
// re-observe a few documents connector A already flushed (duplicates,
// collapsed downstream by EventLog dedup on IdempotencyKey — LOG-03) — but
// it must never permanently skip one (a gap).
func TestMongoIntegration_CrashResume_NoGaps(t *testing.T) {
	uri := mongoTestURI(t)
	cli := connectMongo(t, uri)

	dbName := "kaptanto_it"
	collName := "crash_resume_" + time.Now().Format("150405.000000")
	coll := cli.Database(dbName).Collection(collName)
	t.Cleanup(func() {
		_ = coll.Drop(context.Background())
	})

	store := newFakeStore()
	sourceID := "it_crash_" + collName

	const total = 200
	var seenMu sync.Mutex
	seen := make(map[string]bool, total)

	recordSeen := func(ch <-chan *event.ChangeEvent, stop <-chan struct{}) {
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if seq, ok := extractSeq(ev); ok {
					seenMu.Lock()
					seen[seq] = true
					seenMu.Unlock()
				}
			case <-stop:
				return
			}
		}
	}

	// --- Connector A: consume some (not necessarily all) of a burst of
	// inserts, then "crash" (context cancel) without draining everything. ---
	connA, err := mongodb.New(mongodb.Config{
		URI: uri, Database: dbName, Collections: []string{collName}, SourceID: sourceID,
	}, store, event.NewIDGenerator())
	require.NoError(t, err)

	ctxA, cancelA := context.WithCancel(context.Background())
	runErrA := make(chan error, 1)
	go func() { runErrA <- connA.Run(ctxA) }()
	stopA := make(chan struct{})
	go recordSeen(connA.Events(), stopA)

	// Give the change stream a moment to open before producing writes, so
	// the resume position starts ahead of our operations.
	time.Sleep(2 * time.Second)

	docs := make([]interface{}, total)
	for i := 0; i < total; i++ {
		docs[i] = bson.M{"seq": fmt.Sprintf("seq-%04d", i)}
	}
	_, err = coll.InsertMany(context.Background(), docs)
	require.NoError(t, err, "burst insert of %d documents", total)

	// Let connector A observe *some* of the burst, then simulate a crash by
	// cancelling before the full burst is necessarily flushed.
	time.Sleep(1500 * time.Millisecond)
	cancelA()
	close(stopA)
	select {
	case <-runErrA:
	case <-time.After(5 * time.Second):
		t.Fatal("connector A did not stop after context cancellation")
	}

	seenMu.Lock()
	afterA := len(seen)
	seenMu.Unlock()
	t.Logf("connector A observed %d/%d documents before simulated crash", afterA, total)

	// --- Connector B: resume from the same store. It must eventually
	// observe every document connector A had not durably flushed, with no
	// permanent gaps. ---
	connB, err := mongodb.New(mongodb.Config{
		URI: uri, Database: dbName, Collections: []string{collName}, SourceID: sourceID,
	}, store, event.NewIDGenerator())
	require.NoError(t, err)

	ctxB, cancelB := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelB()
	runErrB := make(chan error, 1)
	go func() { runErrB <- connB.Run(ctxB) }()
	stopB := make(chan struct{})
	go recordSeen(connB.Events(), stopB)

	deadline := time.Now().Add(18 * time.Second)
	for time.Now().Before(deadline) {
		seenMu.Lock()
		n := len(seen)
		seenMu.Unlock()
		if n >= total {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stopB)
	cancelB()
	select {
	case <-runErrB:
	case <-time.After(5 * time.Second):
		t.Fatal("connector B did not stop after context cancellation")
	}

	seenMu.Lock()
	defer seenMu.Unlock()
	var missing []string
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("seq-%04d", i)
		if !seen[key] {
			missing = append(missing, key)
		}
	}
	assert.Empty(t, missing,
		"crash/resume must not permanently lose any document (%d/%d missing after resume); "+
			"batch-granularity checkpointing must only ever cause re-delivery, never a gap",
		len(missing), total)
}
