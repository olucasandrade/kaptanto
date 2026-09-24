package mongodb_test

import (
	"fmt"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	mongodb "github.com/olucasandrade/kaptanto/internal/parser/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestNormalize_MetadataClusterTimeLSN verifies live change events stamp
// metadata["lsn"] with the same T<<32|I uint64 encoding captureSnapshotLSN
// uses (as a decimal string for EventLog JSON safety).
//
// Callers: go test ./internal/parser/mongodb only. Extends NormalizeChangeEvent
// metadata contract; no data-file I/O. User instruction: stamp Mongo events
// with T<<32|I and make watermark accept that value when Postgres lsn is absent.
func TestNormalize_MetadataClusterTimeLSN(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "x", Value: 1}}
	ct := bson.Timestamp{T: 1000, I: 5}
	want := uint64(1000)<<32 | uint64(5)

	doc := bson.D{
		{Key: "_id", Value: bson.D{{Key: "_data", Value: "82AABBCC"}}},
		{Key: "operationType", Value: "insert"},
		{Key: "clusterTime", Value: ct},
		{Key: "ns", Value: bson.D{{Key: "db", Value: "testdb"}, {Key: "coll", Value: "orders"}}},
		{Key: "documentKey", Value: bson.D{{Key: "_id", Value: oid}}},
		{Key: "fullDocument", Value: fullDoc},
	}
	raw, err := bson.Marshal(doc)
	require.NoError(t, err)

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)

	rawLSN, ok := ev.Metadata["lsn"]
	require.True(t, ok, "metadata must contain lsn (clusterTime uint64 encoding)")
	lsnStr, ok := rawLSN.(string)
	require.True(t, ok, "lsn should be a decimal string of the uint64 encoding, got %T", rawLSN)
	assert.Equal(t, fmt.Sprintf("%d", want), lsnStr)
}
