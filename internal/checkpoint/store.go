// Package checkpoint provides durable persistence for the last acknowledged
// source position (LSN for Postgres). The checkpoint store is the durable
// write target required by the critical invariant: the source checkpoint is
// NEVER advanced until after a durable write.
package checkpoint

import "context"

// CheckpointStore persists and retrieves the last acknowledged source
// position (e.g. a Postgres LSN string). Implementations must be safe for
// concurrent use by a single goroutine (the connector owns the store).
type CheckpointStore interface {
	// Save upserts the LSN for the given sourceID. Calling Save twice with the
	// same sourceID updates the stored value — it is idempotent from the
	// connector's perspective.
	Save(ctx context.Context, sourceID, lsn string) error

	// Load returns the stored LSN for sourceID. If no checkpoint exists for
	// sourceID (first run), Load returns ("", nil) — this is not an error.
	Load(ctx context.Context, sourceID string) (string, error)

	// Close flushes all pending writes (WAL checkpoint) and releases the
	// underlying database handle. It must be called on graceful shutdown.
	Close() error
}
