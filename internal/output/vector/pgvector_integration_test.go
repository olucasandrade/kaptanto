package vector_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/output/vector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPGVector_Integration exercises auto-create idempotence and upsert/delete
// round-trip. Skips when POSTGRES_TEST_DSN is unset (repo pattern).
//
// Pinecone and Qdrant backends remain httptest/golden unit tests only — CI has
// no live credentials for those providers. Optional PINECONE_TEST_* / QDRANT_TEST_*
// env-gated live tests may be added later; pgvector coverage here uses the same
// POSTGRES_TEST_DSN service as integration.yml (pgvector/pgvector:pg16).
func TestPGVector_Integration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}
	ctx := context.Background()
	table := "kaptanto_vectors_test_g312"

	// Clean slate.
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, _ = conn.Exec(ctx, `DROP TABLE IF EXISTS `+pgx.Identifier{table}.Sanitize())
	require.NoError(t, conn.Close(ctx))

	// Concurrent OpenPGVector — CREATE TABLE IF NOT EXISTS must succeed for both.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	stores := make([]*vector.PGVectorStore, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := vector.OpenPGVector(ctx, dsn, table, 3)
			errs[i] = err
			stores[i] = s
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "store %d", i)
	}
	t.Cleanup(func() {
		for _, s := range stores {
			if s != nil {
				_ = s.Close()
			}
		}
		c, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_, _ = c.Exec(ctx, `DROP TABLE IF EXISTS `+pgx.Identifier{table}.Sanitize())
			_ = c.Close(ctx)
		}
	})

	s := stores[0]
	require.NoError(t, s.Ping(ctx))

	id1, err := vector.CanonicalID("public", "orders", map[string]any{"id": "1"})
	require.NoError(t, err)
	id2, err := vector.CanonicalID("public", "orders", map[string]any{"id": "2"})
	require.NoError(t, err)

	recs := []vector.Record{
		{ID: id1, Vector: []float32{0.1, 0.2, 0.3}, Text: "one", Metadata: map[string]any{"op": "insert"}},
		{ID: id2, Vector: []float32{0.4, 0.5, 0.6}, Text: "two", Metadata: map[string]any{"op": "update"}},
	}
	require.NoError(t, s.Upsert(ctx, recs))

	// Re-upsert id1 with new vector — idempotent overwrite.
	require.NoError(t, s.Upsert(ctx, []vector.Record{
		{ID: id1, Vector: []float32{0.9, 0.8, 0.7}, Text: "one-v2", Metadata: map[string]any{"op": "update"}},
	}))

	verify := func() {
		c, err := pgx.Connect(ctx, dsn)
		require.NoError(t, err)
		defer func() { _ = c.Close(ctx) }()
		var text string
		var meta []byte
		err = c.QueryRow(ctx,
			`SELECT text, metadata FROM `+pgx.Identifier{table}.Sanitize()+` WHERE id = $1`, id1,
		).Scan(&text, &meta)
		require.NoError(t, err)
		assert.Equal(t, "one-v2", text)
		var m map[string]any
		require.NoError(t, json.Unmarshal(meta, &m))
		assert.Equal(t, "update", m["op"])

		var n int
		err = c.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	}
	verify()

	require.NoError(t, s.Delete(ctx, []string{id1}))
	c, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = c.Close(ctx) }()
	var n int
	err = c.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestOpenPGVector_EmptyOps(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run Postgres integration tests")
	}
	ctx := context.Background()
	s, err := vector.OpenPGVector(ctx, dsn, "kaptanto_vectors_empty_ops", 2)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
		c, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_, _ = c.Exec(ctx, `DROP TABLE IF EXISTS kaptanto_vectors_empty_ops`)
			_ = c.Close(ctx)
		}
	})
	require.NoError(t, s.Upsert(ctx, nil))
	require.NoError(t, s.Delete(ctx, nil))
}
