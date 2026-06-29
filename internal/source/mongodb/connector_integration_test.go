package mongodb_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
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
