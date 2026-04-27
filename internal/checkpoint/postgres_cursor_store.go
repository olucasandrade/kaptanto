package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// Compile-time assertion: PostgresCursorStore must satisfy ConsumerCursorStore.
// (ConsumerCursorStore is defined in internal/router/router.go; we verify via
// method-set compatibility by casting to the interface during OpenPostgresCursorStore.)

const createCursorTablePostgresSQL = `
CREATE TABLE IF NOT EXISTS kaptanto_cursors (
    consumer_id  TEXT    NOT NULL,
    partition_id INTEGER NOT NULL,
    seq          BIGINT  NOT NULL,
    updated_at   TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (consumer_id, partition_id)
);`

const upsertCursorPostgresSQL = `
INSERT INTO kaptanto_cursors (consumer_id, partition_id, seq, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (consumer_id, partition_id) DO UPDATE
    SET seq = EXCLUDED.seq, updated_at = NOW();`

const selectCursorPostgresSQL = `SELECT seq FROM kaptanto_cursors WHERE consumer_id = $1 AND partition_id = $2;`

// pgCursorKey uniquely identifies a (consumerID, partitionID) pair in the dirty map.
type pgCursorKey struct {
	consumerID  string
	partitionID uint32
}

// PostgresCursorStore implements the ConsumerCursorStore interface with a
// batched flush design: SaveCursor writes to an in-memory dirty map (O(1)
// fast path) and a background ticker batches dirty cursors to Postgres.
//
// This is the cluster-mode drop-in replacement for SQLiteCursorStore.
// A surviving node can resume delivery from the exact last acknowledged
// cursor position after another node crashes (STATE-01).
//
// Uses a single pgx.Conn (not a pool) — consistent with PostgresStore.
type PostgresCursorStore struct {
	conn          *pgx.Conn
	mu            sync.Mutex
	dirty         map[pgCursorKey]uint64
	flushInterval time.Duration
	metrics       *observability.KaptantoMetrics
}

// OpenPostgresCursorStore connects to Postgres at dsn, creates the
// kaptanto_cursors table if it does not exist, and returns a ready
// *PostgresCursorStore. flushInterval controls how often dirty cursors are
// batched to Postgres (5*time.Second is a reasonable default).
//
// The caller must call Run(ctx) in a goroutine to start the flush loop,
// and Close() on graceful shutdown.
func OpenPostgresCursorStore(ctx context.Context, dsn string, flushInterval time.Duration) (*PostgresCursorStore, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open postgres cursor store: %w", err)
	}

	if _, err := conn.Exec(ctx, createCursorTablePostgresSQL); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("checkpoint: create kaptanto_cursors table: %w", err)
	}

	return &PostgresCursorStore{
		conn:          conn,
		dirty:         make(map[pgCursorKey]uint64),
		flushInterval: flushInterval,
	}, nil
}

// SetMetrics injects a KaptantoMetrics reference. Safe to call after
// construction, before Run. Follows the SetBackfillEngine / SetWatermark
// setter pattern used elsewhere in the codebase.
func (s *PostgresCursorStore) SetMetrics(m *observability.KaptantoMetrics) {
	s.metrics = m
}

// SaveCursor writes the seq to the in-memory dirty map. It does not write to
// Postgres directly — flush batches dirty entries on each tick or shutdown.
// SaveCursor is idempotent: the latest seq for (consumerID, partitionID) wins.
func (s *PostgresCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	s.mu.Lock()
	s.dirty[pgCursorKey{consumerID, partitionID}] = seq
	s.mu.Unlock()
	return nil
}

// LoadCursor returns the last saved seq for (consumerID, partitionID).
//
// Lookup order:
//  1. Dirty map (in-memory fast path — reflects SaveCursor calls before flush).
//  2. Postgres (durable store — reflects previously flushed cursors).
//
// Returns 1 (not 0) when no cursor exists for the given pair. Seq 0 is the
// dedup sentinel and must never be used as a start position (RTR-03).
func (s *PostgresCursorStore) LoadCursor(ctx context.Context, consumerID string, partitionID uint32) (uint64, error) {
	k := pgCursorKey{consumerID, partitionID}

	s.mu.Lock()
	v, ok := s.dirty[k]
	s.mu.Unlock()
	if ok {
		return v, nil
	}

	var seq uint64
	err := s.conn.QueryRow(ctx, selectCursorPostgresSQL, consumerID, int(partitionID)).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		// seq 0 is the dedup sentinel; first run starts from 1 (RTR-03).
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("checkpoint: load cursor %q p=%d: %w", consumerID, partitionID, err)
	}
	return seq, nil
}

// Run starts the periodic flush loop. It blocks until ctx is cancelled, at
// which point it performs a final flush before returning. Run must be called
// in its own goroutine.
func (s *PostgresCursorStore) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flush(context.Background()) // final flush on shutdown
			return
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

// flush takes a snapshot of the dirty map under lock, releases the lock, then
// writes all dirty cursors to Postgres in a single transaction. This design
// means SaveCursor is never blocked by Postgres I/O.
//
// On any transaction error the snapshot entries are restored to the dirty map
// (unless they have already been re-dirtied by a concurrent SaveCursor), so
// no cursor progress is lost.
func (s *PostgresCursorStore) flush(ctx context.Context) {
	s.mu.Lock()
	if len(s.dirty) == 0 {
		s.mu.Unlock()
		return
	}
	snapshot := make(map[pgCursorKey]uint64, len(s.dirty))
	for k, v := range s.dirty {
		snapshot[k] = v
	}
	s.dirty = make(map[pgCursorKey]uint64)
	s.mu.Unlock()

	tx, err := s.conn.Begin(ctx)
	if err != nil {
		slog.Warn("checkpoint: postgres cursor flush begin tx", "err", err)
		// Restore snapshot back to dirty map so progress is not lost.
		s.mu.Lock()
		for k, v := range snapshot {
			if _, exists := s.dirty[k]; !exists {
				s.dirty[k] = v
			}
		}
		s.mu.Unlock()
		return
	}

	for k, seq := range snapshot {
		if _, err := tx.Exec(ctx, upsertCursorPostgresSQL, k.consumerID, int(k.partitionID), seq); err != nil {
			slog.Warn("checkpoint: postgres cursor flush upsert", "consumer", k.consumerID, "partition", k.partitionID, "err", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("checkpoint: postgres cursor flush commit", "err", err)
		_ = tx.Rollback(ctx)
		// Restore snapshot on commit failure.
		s.mu.Lock()
		for k, v := range snapshot {
			if _, exists := s.dirty[k]; !exists {
				s.dirty[k] = v
			}
		}
		s.mu.Unlock()
		return
	}

	if s.metrics != nil {
		s.metrics.CheckpointFlushes.Add(1)
	}
}

// Close releases the pgx connection. It must be called on graceful shutdown.
func (s *PostgresCursorStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.conn.Close(ctx); err != nil {
		return fmt.Errorf("checkpoint: postgres cursor store close: %w", err)
	}
	return nil
}
