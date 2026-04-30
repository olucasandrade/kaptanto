// Package cluster provides distributed coordination primitives for Kaptanto.
// WalLeaderElector implements WAL source leader election using NATS JetStream
// KV TTL lease semantics — equivalent to etcd's session-based leader election
// but with zero additional infrastructure beyond the embedded NATS server used
// by NatsEventLog (Phase 15).
//
// SRCC-02: WAL source is a single-writer protocol constraint; exactly one node
// at a time may hold the replication slot. WalLeaderElector enforces this via
// the WAL_LEADER_LEASE KV bucket with a TTL of 15s and renewal every 7s.
// TTL = 2× renewEvery ensures one missed renewal does not evict the leader.
//
// Invariants:
//   - epoch and isLeader are stored in atomic fields — no mutex on hot read path.
//   - The connector context is never cancelled from inside WalLeaderElector;
//     only isLeader is set to false, so the WAL connection continues cleanly.
//   - kv.Create is used for initial acquisition (atomic, returns error if key exists).
//   - kv.Update is used for renewal (CAS on revision — prevents split-brain).
//   - The bucket is opened idempotently (try KeyValue first, create only on error).
package cluster

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// walLeaderBucket is the NATS JetStream KV bucket name for the WAL leader lease.
	walLeaderBucket = "WAL_LEADER_LEASE"

	// walLeaderKey is the key within the bucket that the leader holds.
	walLeaderKey = "leader"

	// defaultLeaseTTL is the TTL for the KV key. NATS KV deletes the key automatically
	// when the TTL elapses without a renewal. Set to 2× defaultRenewEvery so one missed
	// renewal heartbeat does not evict the leader prematurely.
	defaultLeaseTTL = 15 * time.Second

	// defaultRenewEvery is how often the leader calls kv.Update to renew the lease.
	// Must be strictly less than defaultLeaseTTL / 2 so there is headroom for one
	// missed renewal.
	defaultRenewEvery = 7 * time.Second
)

// WalLeaderElector acquires and maintains a WAL leader lease in NATS JetStream KV.
// Exactly one node in the cluster holds the WAL_LEADER_LEASE key at any time.
// The leader is the only node that should start the Postgres WAL connector.
//
// Use NewWalLeaderElector to construct. Call Run in a goroutine (e.g. errgroup.Go).
// EpochGetter is safe to call from any goroutine.
type WalLeaderElector struct {
	nodeID     string
	kv         jetstream.KeyValue
	leaseTTL   time.Duration
	renewEvery time.Duration

	// epoch is the KV revision returned by the last successful Create or Update.
	// Atomically read by EpochGetter; written only by the Run goroutine.
	epoch atomic.Uint64

	// isLeader tracks whether this node currently holds the lease.
	// Atomically read by EpochGetter; written by Run and holdLease goroutines.
	isLeader atomic.Bool
}

// NewWalLeaderElector creates a WalLeaderElector that will compete for the
// WAL_LEADER_LEASE KV bucket lease using the JetStream handle derived from nc.
//
// The bucket is opened idempotently: if WAL_LEADER_LEASE already exists (e.g. another
// node created it first), the existing bucket is reused. The bucket TTL is set to
// defaultLeaseTTL (15s); the bucket TTL is not reconfigured if it already exists
// (first writer's config wins — nodes must agree on TTL via config).
func NewWalLeaderElector(ctx context.Context, nc *nats.Conn, nodeID string) (*WalLeaderElector, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("wal leader: jetstream context: %w", err)
	}

	kv, err := openOrCreateLeaderBucket(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("wal leader: open bucket: %w", err)
	}

	return &WalLeaderElector{
		nodeID:     nodeID,
		kv:         kv,
		leaseTTL:   defaultLeaseTTL,
		renewEvery: defaultRenewEvery,
	}, nil
}

// openOrCreateLeaderBucket opens the WAL_LEADER_LEASE bucket if it already exists,
// or creates it with the correct TTL if not. This is idempotent — concurrent nodes
// racing to create the bucket will both succeed (one creates, the others open).
func openOrCreateLeaderBucket(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	// Try to open an existing bucket first. This is the fast path when the cluster
	// has already been initialised (node restart, rolling upgrade).
	kv, err := js.KeyValue(ctx, walLeaderBucket)
	if err == nil {
		return kv, nil
	}

	// Bucket does not exist yet — create it. The TTL is set to defaultLeaseTTL.
	// History=1: we only need the current value; retaining history wastes storage.
	kv, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  walLeaderBucket,
		TTL:     defaultLeaseTTL,
		History: 1,
	})
	if err != nil {
		// Another node may have created the bucket between our KeyValue call and here.
		// Retry the open — if it succeeds the race resolved correctly.
		kv2, err2 := js.KeyValue(ctx, walLeaderBucket)
		if err2 == nil {
			return kv2, nil
		}
		// Both attempts failed — surface the original creation error.
		return nil, fmt.Errorf("create KV bucket: %w", err)
	}
	return kv, nil
}

// Run acquires or watches the WAL leader lease until ctx is cancelled.
// It blocks, so it should be called in an errgroup goroutine.
//
// Flow:
//  1. Attempt kv.Create to acquire the lease atomically.
//  2. On success: call holdLease to renew every renewEvery.
//     holdLease returns when context is cancelled or CAS renewal fails.
//  3. On failure (key already exists): watch for key deletion/expiry, then loop.
//  4. On ctx.Done(): set isLeader=false, return ctx.Err().
func (e *WalLeaderElector) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			e.isLeader.Store(false)
			return ctx.Err()
		default:
		}

		// Attempt to create the lease key. This is atomic — only one node succeeds.
		rev, err := e.kv.Create(ctx, walLeaderKey, []byte(e.nodeID))
		if err == nil {
			// We won the lease.
			e.epoch.Store(rev)
			e.isLeader.Store(true)

			// holdLease renews until ctx is cancelled or CAS fails.
			if holdErr := e.holdLease(ctx, rev); holdErr != nil {
				// CAS failure or context cancel. isLeader already set to false by holdLease.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// CAS conflict: another node took over. Enter the watch loop below.
			}
			continue
		}

		// Lease is held by another node. Watch for expiry/deletion.
		if watchErr := e.watchForExpiry(ctx); watchErr != nil {
			if ctx.Err() != nil {
				e.isLeader.Store(false)
				return ctx.Err()
			}
			// Watch ended for another reason — retry the create loop.
		}
	}
}

// holdLease renews the lease every renewEvery via kv.Update (CAS on revision).
// Returns when ctx is cancelled or the CAS renewal fails (e.g. another node took over).
// Sets isLeader=false before returning in both cases.
func (e *WalLeaderElector) holdLease(ctx context.Context, rev uint64) error {
	ticker := time.NewTicker(e.renewEvery)
	defer ticker.Stop()

	currentRev := rev
	for {
		select {
		case <-ctx.Done():
			e.isLeader.Store(false)
			return ctx.Err()
		case <-ticker.C:
			newRev, err := e.kv.Update(ctx, walLeaderKey, []byte(e.nodeID), currentRev)
			if err != nil {
				// CAS mismatch or context cancel — we lost the lease.
				e.isLeader.Store(false)
				return fmt.Errorf("wal leader: renewal CAS failed: %w", err)
			}
			currentRev = newRev
			e.epoch.Store(newRev)
		}
	}
}

// watchForExpiry watches the leader KV key until it is deleted or expires (TTL),
// or until ctx is cancelled. Returns nil when the key is gone (ready to re-elect),
// ctx.Err() when cancelled.
func (e *WalLeaderElector) watchForExpiry(ctx context.Context) error {
	watcher, err := e.kv.Watch(ctx, walLeaderKey)
	if err != nil {
		return fmt.Errorf("wal leader: watch: %w", err)
	}
	defer watcher.Stop() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-watcher.Updates():
			if !ok {
				// Channel closed — watcher stopped.
				return nil
			}
			if entry == nil {
				// Initial value delivered; nil means no current entry (key expired or deleted).
				return nil
			}
			if entry.Operation() == jetstream.KeyValueDelete ||
				entry.Operation() == jetstream.KeyValuePurge {
				return nil
			}
			// Key still held by another node — continue watching.
		}
	}
}

// EpochGetter returns the current (epoch, isLeader) pair atomically.
// Safe to call from any goroutine — reads atomic fields with no locks.
//
// The epoch value is the NATS KV revision returned by the last successful
// Create or Update. It increases monotonically across leader changes,
// providing a fencing token for use by PostgresConnector.SetEpochGetter.
//
// Returns (0, false) for a non-leader node.
func (e *WalLeaderElector) EpochGetter() (uint64, bool) {
	return e.epoch.Load(), e.isLeader.Load()
}
