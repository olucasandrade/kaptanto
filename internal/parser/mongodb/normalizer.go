// Package mongodb provides the BSON normalizer for MongoDB Change Stream events.
// It converts raw bson.Raw Change Stream documents into the unified ChangeEvent format.
package mongodb

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaptanto/kaptanto/internal/event"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// changeStreamDoc is a typed struct for decoding the top-level fields of a
// MongoDB Change Stream event.
type changeStreamDoc struct {
	// ResumeToken is the resume token (the "_id" field of the change stream event).
	ResumeToken bson.Raw `bson:"_id"`

	OperationType string `bson:"operationType"`

	ClusterTime bson.Timestamp `bson:"clusterTime"`

	NS struct {
		DB   string `bson:"db"`
		Coll string `bson:"coll"`
	} `bson:"ns"`

	DocumentKey bson.Raw `bson:"documentKey"`

	FullDocument             bson.Raw `bson:"fullDocument"`
	FullDocumentBeforeChange bson.Raw `bson:"fullDocumentBeforeChange"`
}

// NormalizeChangeEvent converts a raw MongoDB Change Stream event (bson.Raw)
// into a unified ChangeEvent. The sourceID is embedded in the idempotency key.
// idGen produces the ULID event ID.
func NormalizeChangeEvent(raw bson.Raw, sourceID string, idGen *event.IDGenerator) (*event.ChangeEvent, error) {
	var cs changeStreamDoc
	if err := bson.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("mongodb: failed to decode change stream event: %w", err)
	}

	// Map operationType to event.Operation.
	var op event.Operation
	switch cs.OperationType {
	case "insert":
		op = event.OpInsert
	case "update", "replace":
		op = event.OpUpdate
	case "delete":
		op = event.OpDelete
	default:
		return nil, fmt.Errorf("mongodb: unsupported operationType %q", cs.OperationType)
	}

	// Validate: insert must have a fullDocument.
	if op == event.OpInsert && len(cs.FullDocument) == 0 {
		return nil, fmt.Errorf("mongodb: insert event missing fullDocument")
	}

	// Serialize documentKey to extended JSON.
	keyJSON, err := marshalExtJSON(cs.DocumentKey)
	if err != nil {
		return nil, fmt.Errorf("mongodb: failed to serialize documentKey: %w", err)
	}

	// Serialize fullDocument → After.
	var afterJSON json.RawMessage
	if len(cs.FullDocument) > 0 {
		b, err := marshalExtJSON(cs.FullDocument)
		if err != nil {
			return nil, fmt.Errorf("mongodb: failed to serialize fullDocument: %w", err)
		}
		afterJSON = b
	}

	// Serialize fullDocumentBeforeChange → Before.
	var beforeJSON json.RawMessage
	if len(cs.FullDocumentBeforeChange) > 0 {
		b, err := marshalExtJSON(cs.FullDocumentBeforeChange)
		if err != nil {
			return nil, fmt.Errorf("mongodb: failed to serialize fullDocumentBeforeChange: %w", err)
		}
		beforeJSON = b
	}

	// Timestamp from clusterTime (BSON Timestamp: T = seconds since Unix epoch).
	ts := time.Unix(int64(cs.ClusterTime.T), 0).UTC()

	// Resume token: serialize the _id field as a string.
	resumeToken := cs.ResumeToken.String()

	// IdempotencyKey: "<sourceID>:<db>.<coll>:<documentKey_canonical_json>:<operation>:<clusterTime_hex>"
	idempotencyKey := fmt.Sprintf("%s:%s.%s:%s:%s:%08X%08X",
		sourceID,
		cs.NS.DB, cs.NS.Coll,
		string(keyJSON),
		string(op),
		cs.ClusterTime.T, cs.ClusterTime.I,
	)

	ev := &event.ChangeEvent{
		ID:             idGen.New(),
		IdempotencyKey: idempotencyKey,
		Timestamp:      ts,
		Source:         sourceID,
		Operation:      op,
		Database:       cs.NS.DB,
		Schema:         "",
		Table:          cs.NS.Coll,
		Key:            json.RawMessage(keyJSON),
		Before:         beforeJSON,
		After:          afterJSON,
		Metadata: map[string]any{
			"resume_token": resumeToken,
			"snapshot":     false,
			"db":           cs.NS.DB,
			"collection":   cs.NS.Coll,
		},
	}

	return ev, nil
}

// marshalExtJSON serializes a bson.Raw document as canonical extended JSON.
// canonical=true preserves BSON type wrappers (e.g., {"$oid":"..."} for ObjectID).
// escapeHTML=false avoids unnecessary HTML escaping in JSON strings.
func marshalExtJSON(doc bson.Raw) ([]byte, error) {
	return bson.MarshalExtJSON(doc, true, false)
}
