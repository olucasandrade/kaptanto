package mongodb_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaptanto/kaptanto/internal/event"
	mongodb "github.com/kaptanto/kaptanto/internal/parser/mongodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// buildRaw marshals a bson.D into bson.Raw.
func buildRaw(t *testing.T, d bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(d)
	require.NoError(t, err)
	return bson.Raw(b)
}

// buildChangeStreamDoc builds a synthetic MongoDB Change Stream document.
func buildChangeStreamDoc(t *testing.T, opts map[string]interface{}) bson.Raw {
	t.Helper()

	oid := bson.NewObjectID()
	if v, ok := opts["oid"]; ok {
		oid = v.(bson.ObjectID)
	}

	opType := "insert"
	if v, ok := opts["operationType"]; ok {
		opType = v.(string)
	}

	db := "testdb"
	if v, ok := opts["db"]; ok {
		db = v.(string)
	}
	coll := "orders"
	if v, ok := opts["coll"]; ok {
		coll = v.(string)
	}

	// clusterTime: T=seconds, I=ordinal
	clusterTime := bson.Timestamp{T: uint32(time.Now().Unix()), I: 1}

	// resume token (_id)
	resumeToken := bson.D{{Key: "_data", Value: "82AABBCC"}}

	doc := bson.D{
		{Key: "_id", Value: resumeToken},
		{Key: "operationType", Value: opType},
		{Key: "clusterTime", Value: clusterTime},
		{Key: "ns", Value: bson.D{{Key: "db", Value: db}, {Key: "coll", Value: coll}}},
		{Key: "documentKey", Value: bson.D{{Key: "_id", Value: oid}}},
	}

	if v, ok := opts["fullDocument"]; ok {
		doc = append(doc, bson.E{Key: "fullDocument", Value: v})
	}
	if v, ok := opts["fullDocumentBeforeChange"]; ok {
		doc = append(doc, bson.E{Key: "fullDocumentBeforeChange", Value: v})
	}

	return buildRaw(t, doc)
}

func TestNormalize_Insert(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "amount", Value: 100.0}, {Key: "status", Value: "pending"}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "insert",
		"oid":           oid,
		"fullDocument":  fullDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpInsert, ev.Operation)
	assert.Equal(t, "orders", ev.Table)
	assert.Equal(t, "testdb", ev.Database)
	assert.Equal(t, "", ev.Schema)
	assert.Equal(t, "src1", ev.Source)

	// After should be non-nil JSON containing the fullDocument
	require.NotNil(t, ev.After)
	var afterMap map[string]interface{}
	require.NoError(t, json.Unmarshal(ev.After, &afterMap))
	assert.Contains(t, string(ev.After), "$oid", "ObjectID should use extended JSON")

	// Before should be nil (JSON null)
	assert.Nil(t, ev.Before)

	// Key should contain the ObjectID as extended JSON
	require.NotNil(t, ev.Key)
	assert.Contains(t, string(ev.Key), "$oid", "key should use extended JSON for ObjectID")

	// Metadata
	assert.Equal(t, false, ev.Metadata["snapshot"])
	assert.Equal(t, "orders", ev.Metadata["collection"])
	assert.Equal(t, "testdb", ev.Metadata["db"])
}

func TestNormalize_Update_WithBefore(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "amount", Value: 200.0}}
	beforeDoc := bson.D{{Key: "_id", Value: oid}, {Key: "amount", Value: 100.0}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType":            "update",
		"oid":                      oid,
		"fullDocument":             fullDoc,
		"fullDocumentBeforeChange": beforeDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpUpdate, ev.Operation)
	assert.NotNil(t, ev.After, "after should be populated for update")
	assert.NotNil(t, ev.Before, "before should be populated when fullDocumentBeforeChange present")

	var beforeMap map[string]interface{}
	require.NoError(t, json.Unmarshal(ev.Before, &beforeMap))
	assert.Contains(t, string(ev.Before), "$oid")
}

func TestNormalize_Update_NoBefore(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "amount", Value: 200.0}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "update",
		"oid":           oid,
		"fullDocument":  fullDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpUpdate, ev.Operation)
	assert.NotNil(t, ev.After)
	assert.Nil(t, ev.Before, "before should be nil when fullDocumentBeforeChange absent")
}

func TestNormalize_Delete(t *testing.T) {
	oid := bson.NewObjectID()

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "delete",
		"oid":           oid,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)
	require.NotNil(t, ev)

	assert.Equal(t, event.OpDelete, ev.Operation)
	assert.NotNil(t, ev.Key)
	assert.Nil(t, ev.After, "after should be nil for delete")
	assert.Nil(t, ev.Before, "before should be nil for delete (no fullDocumentBeforeChange)")
}

func TestNormalize_Replace(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "amount", Value: 300.0}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "replace",
		"oid":           oid,
		"fullDocument":  fullDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)
	require.NotNil(t, ev)

	// replace is treated as update
	assert.Equal(t, event.OpUpdate, ev.Operation)
	assert.NotNil(t, ev.After)
}

func TestNormalize_UnknownOpType(t *testing.T) {
	oid := bson.NewObjectID()

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "invalidate",
		"oid":           oid,
	})

	idGen := event.NewIDGenerator()
	_, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported operationType")
}

func TestNormalize_InsertMissingFullDocument(t *testing.T) {
	oid := bson.NewObjectID()

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "insert",
		"oid":           oid,
		// no fullDocument
	})

	idGen := event.NewIDGenerator()
	_, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing fullDocument")
}

func TestNormalize_MetadataResumeToken(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "x", Value: 1}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "insert",
		"oid":           oid,
		"fullDocument":  fullDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "src1", idGen)
	require.NoError(t, err)

	token, ok := ev.Metadata["resume_token"]
	require.True(t, ok, "metadata should contain resume_token")
	assert.NotEmpty(t, token, "resume_token should be non-empty")
}

func TestNormalize_IdempotencyKey(t *testing.T) {
	oid := bson.NewObjectID()
	fullDoc := bson.D{{Key: "_id", Value: oid}, {Key: "x", Value: 1}}

	raw := buildChangeStreamDoc(t, map[string]interface{}{
		"operationType": "insert",
		"oid":           oid,
		"fullDocument":  fullDoc,
	})

	idGen := event.NewIDGenerator()
	ev, err := mongodb.NormalizeChangeEvent(raw, "mysource", idGen)
	require.NoError(t, err)

	assert.NotEmpty(t, ev.IdempotencyKey, "idempotency key should not be empty")
	assert.True(t, strings.HasPrefix(ev.IdempotencyKey, "mysource:"), "idempotency key should start with sourceID")
	assert.Contains(t, ev.IdempotencyKey, "insert", "idempotency key should contain operation")
}
