package loadgen

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS bench_events (
    id          TEXT        NOT NULL PRIMARY KEY,
    payload     TEXT        NOT NULL DEFAULT '',
    _bench_ts   TIMESTAMPTZ NOT NULL
);`

const createPublicationSQL = `
CREATE PUBLICATION IF NOT EXISTS bench_pub FOR TABLE bench_events;`

// EnsureSchema creates the bench_events table and bench_pub publication
// if they do not already exist. Safe to call on every loadgen startup.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, createTableSQL); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	if _, err := conn.Exec(ctx, createPublicationSQL); err != nil {
		return fmt.Errorf("create publication: %w", err)
	}

	return nil
}

// GeneratePayload returns a string of approximately sizeBytes length.
func GeneratePayload(sizeBytes int) string {
	if sizeBytes <= 0 {
		return ""
	}
	return strings.Repeat("x", sizeBytes)
}

// GenerateID returns a random hex string suitable for use as a TEXT primary key.
func GenerateID() string {
	return fmt.Sprintf("%x", rand.Int63())
}
