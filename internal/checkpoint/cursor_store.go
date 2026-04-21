package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/observability"
)

const createCursorTableSQL = `
CREATE TABLE IF NOT EXISTS consumer_cursors (
    consumer_id  TEXT NOT NULL,
    partition_id INTEGER NOT NULL,
    seq          INTEGER NOT NULL,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (consumer_id, partition_id)
);`

const upsertCursorSQL = `
INSERT INTO consumer_cursors (consumer_id, partition_id, seq)
VALUES (?, ?, ?)
ON CONFLICT(consumer_id, partition_id) DO UPDATE
    SET seq = excluded.seq, updated_at = CURRENT_TIMESTAMP;`

const selectCursorSQL = `
SELECT seq FROM consumer_cursors WHERE consumer_id = ? AND partition_id = ?;`

// cursorKey uniquely identifies a (consumerID, partitionID) pair in the dirty map.
type cursorKey struct {
	consumerID  string
	partitionID uint32
}

// SQLiteCursorStore implements router.ConsumerCursorStore with a batched flush
// design: SaveCursor writes to an in-memory dirty map (O(1) fast path) and a
// background ticker batches dirty cursors to SQLite. Run must be called in a
// goroutine to start the flush loop.
type SQLiteCursorStore struct {
	db            *sql.DB
	mu            sync.Mutex
	dirty         map[cursorKey]uint64
	flushInterval time.Duration
	metrics       *observability.KaptantoMetrics
}

// NewSQLiteCursorStore creates a SQLiteCursorStore using the provided
// already-open *sql.DB. It creates the consumer_cursors table if it does not
// exist. The caller owns the *sql.DB lifecycle (open and close).
//
// flushInterval controls how often dirty cursors are batched to SQLite.
// Pass 5*time.Second as a reasonable default.
func NewSQLiteCursorStore(db *sql.DB, flushInterval time.Duration) (*SQLiteCursorStore, error) {
	if _, err := db.Exec(createCursorTableSQL); err != nil {
		return nil, fmt.Errorf("checkpoint: create consumer_cursors table: %w", err)
	}
	return &SQLiteCursorStore{
		db:            db,
		dirty:         make(map[cursorKey]uint64),
		flushInterval: flushInterval,
	}, nil
}

// SetMetrics injects a KaptantoMetrics reference. Safe to call after construction,
// before Run. Follows the SetBackfillEngine / SetWatermark setter pattern.
func (s *SQLiteCursorStore) SetMetrics(m *observability.KaptantoMetrics) {
	s.metrics = m
}

// Ping checks SQLite cursor store connectivity.
func (s *SQLiteCursorStore) Ping() error {
	return s.db.PingContext(context.Background())
}

// SaveCursor writes the seq to the in-memory dirty map. It does not write to
// SQLite directly — flush batches dirty entries on each tick or shutdown.
// SaveCursor is idempotent: the latest seq for (consumerID, partitionID) wins.
func (s *SQLiteCursorStore) SaveCursor(_ context.Context, consumerID string, partitionID uint32, seq uint64) error {
	s.mu.Lock()
	s.dirty[cursorKey{consumerID, partitionID}] = seq
	s.mu.Unlock()
	return nil
}

// LoadCursor returns the last saved seq for (consumerID, partitionID).
//
// Lookup order:
//  1. Dirty map (in-memory fast path — reflects SaveCursor calls before flush).
//  2. SQLite (durable store — reflects previously flushed cursors).
//
// Returns 1 (not 0) when no cursor exists for the given pair. Seq 0 is the
// dedup sentinel and must never be used as a start position (RTR-03).
func (s *SQLiteCursorStore) LoadCursor(ctx context.Context, consumerID string, partitionID uint32) (uint64, error) {
	k := cursorKey{consumerID, partitionID}

	s.mu.Lock()
	v, ok := s.dirty[k]
	s.mu.Unlock()
	if ok {
		return v, nil
	}

	var seq uint64
	err := s.db.QueryRowContext(ctx, selectCursorSQL, consumerID, int(partitionID)).Scan(&seq)
	if err == sql.ErrNoRows {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("checkpoint: load cursor %q p=%d: %w", consumerID, partitionID, err)
	}
	return seq, nil
}

// Run starts the periodic flush loop. It blocks until ctx is cancelled, at
// which point it performs a final flush before returning. Run must be called in
// its own goroutine.
func (s *SQLiteCursorStore) Run(ctx context.Context) {
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
// writes all dirty cursors to SQLite in a single transaction. This design means
// SaveCursor is never blocked by SQLite I/O.
func (s *SQLiteCursorStore) flush(ctx context.Context) {
	s.mu.Lock()
	if len(s.dirty) == 0 {
		s.mu.Unlock()
		return
	}
	snapshot := make(map[cursorKey]uint64, len(s.dirty))
	for k, v := range s.dirty {
		snapshot[k] = v
	}
	s.dirty = make(map[cursorKey]uint64)
	s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("checkpoint: flush begin tx", "err", err)
		// Restore snapshot back to dirty map so it isn't lost.
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
		if _, err := tx.ExecContext(ctx, upsertCursorSQL, k.consumerID, k.partitionID, seq); err != nil {
			slog.Warn("checkpoint: flush upsert", "consumer", k.consumerID, "partition", k.partitionID, "err", err)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("checkpoint: flush commit", "err", err)
		_ = tx.Rollback()
		return
	}
	if s.metrics != nil {
		s.metrics.CheckpointFlushes.Add(1)
	}
}
