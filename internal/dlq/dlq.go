// Package dlq provides a dead-letter queue for poison CDC events that exhaust
// router retries.
//
// Invariants:
//
//   - DLQ-01: an event leaves the retry/blocked state only after it is
//     delivered OR durably written to a DLQ store.
//   - DLQ-02: DLQ writes dedup on (consumer_id, event_id); re-dead-lettering
//     after a crash is a no-op.
package dlq

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Store.Get when no entry exists for the given ID.
var ErrNotFound = errors.New("dlq: entry not found")

// Entry is a single dead-lettered event parked for later inspection or replay.
//
// Payload is the raw, PRE-transform ChangeEvent JSON so replay can re-run the
// current config's transform (a transform-poisoned event is fixable by editing
// the expression and replaying).
type Entry struct {
	ID             string // ULID minted at Write time when empty
	ConsumerID     string
	EventID        string // ChangeEvent ULID
	Table          string
	PartitionID    uint32
	Seq            uint64
	Attempts       int
	Reason         string // final error string, truncated to 1024 bytes on Write
	IdempotencyKey string
	Payload        []byte // RAW pre-transform ChangeEvent JSON
	CreatedAt      time.Time
}

// Filter selects a subset of DLQ entries for List and Purge.
//
// Zero-value fields are unconstrained: empty ConsumerID/Table match all,
// zero OlderThan applies no time bound, and Limit 0 means no limit.
type Filter struct {
	ConsumerID string    // "" = all
	Table      string    // "" = all
	OlderThan  time.Time // zero = no bound
	Limit      int       // 0 = no limit (the CLI defaults List to 100, not this package)
}

// Store is the persistence contract for dead-lettered events.
//
// Write is idempotent on (ConsumerID, EventID) per DLQ-02. List returns entries
// ordered by ConsumerID, PartitionID, Seq.
type Store interface {
	Write(ctx context.Context, e Entry) error
	List(ctx context.Context, f Filter) ([]Entry, error)
	Get(ctx context.Context, id string) (Entry, error)
	Delete(ctx context.Context, ids ...string) error
	Purge(ctx context.Context, f Filter) (int, error)
	Close() error
}
