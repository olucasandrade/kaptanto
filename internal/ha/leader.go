// Package ha provides leader election for high-availability deployments of
// Kaptanto. Two instances compete for a single Postgres session-scoped advisory
// lock; exactly one holds it at any time. When the lock-holder crashes, Postgres
// automatically releases the lock so the standby can take over.
package ha

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// haLockID is the well-known advisory lock key shared by all Kaptanto instances.
// The value is "KAPTANTO" encoded as a big-endian int64.
const haLockID int64 = 0x4B415054414E544F

// LeaderElector holds a dedicated Postgres connection whose session lifetime
// defines the lock tenure. The lock is session-scoped: Postgres releases it
// automatically when the connection is closed — no heartbeat or TTL needed.
type LeaderElector struct {
	conn *pgx.Conn
}

// NewLeaderElector opens a dedicated pgx.Conn for leader election. This
// connection is separate from the replication connection and the checkpoint
// store connection so that the lock can be held for the full process lifetime.
func NewLeaderElector(ctx context.Context, dsn string) (*LeaderElector, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &LeaderElector{conn: conn}, nil
}

// TryAcquire attempts to acquire the advisory lock. Returns (true, nil) if the
// lock was acquired, (false, nil) if another session holds it, or (false, err)
// on a database error.
func (le *LeaderElector) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	err := le.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", haLockID).Scan(&acquired)
	if err != nil {
		return false, err
	}
	return acquired, nil
}

// Release explicitly releases the advisory lock. Returns an error if the
// unlock call fails.
func (le *LeaderElector) Release(ctx context.Context) error {
	var ok bool
	err := le.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", haLockID).Scan(&ok)
	if err != nil {
		return err
	}
	return nil
}

// RunStandby polls for the advisory lock every pollInterval until either the
// context is cancelled or the lock is acquired. Returns nil to signal "I am now
// the leader", or ctx.Err() if the context was cancelled first.
//
// Transient database errors during polling are logged and skipped — a brief DB
// hiccup should not abort the standby loop.
func (le *LeaderElector) RunStandby(ctx context.Context, pollInterval time.Duration) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			acquired, err := le.TryAcquire(ctx)
			if err != nil {
				slog.Warn("ha: transient error polling advisory lock", "err", err)
				continue
			}
			if acquired {
				return nil
			}
		}
	}
}

// Close closes the underlying Postgres connection. Postgres automatically
// releases the session-scoped advisory lock when the connection is closed.
func (le *LeaderElector) Close() {
	if le.conn != nil {
		le.conn.Close(context.Background())
	}
}
