//go:build e2e

// Package e2e exercises the kaptanto binary end-to-end against a live Postgres
// using logical replication. It builds the binary, streams to stdout (NDJSON),
// drives INSERT/UPDATE/DELETE through a real connection, and asserts the
// resulting ChangeEvents arrive in per-key order with the correct operations.
//
// Requires a Postgres with wal_level=logical. Set POSTGRES_TEST_DSN, e.g.:
//
//	POSTGRES_TEST_DSN="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//	  go test -tags e2e ./test/e2e/...
//
// The e2e workflow (.github/workflows/e2e.yml) provisions Postgres and sets it.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

// buildBinary compiles the kaptanto binary to a temp path and returns it.
func buildBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "kaptanto")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/kaptanto")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "build kaptanto: %s", out)
	return bin
}

func TestE2E_Postgres_CRUDStream(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (logical-replication Postgres) to run e2e tests")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)

	// Unique names so concurrent/repeat runs do not collide on slot/publication.
	suffix := time.Now().Format("150405000")
	table := "e2e_" + suffix
	sourceID := "e2e_" + suffix

	_, err = conn.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (id int PRIMARY KEY, status text)", table))
	require.NoError(t, err)

	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS public.%s", table))
		// Drop the replication slot/publication kaptanto creates (best effort).
		_, _ = c.Exec(context.Background(),
			fmt.Sprintf("SELECT pg_drop_replication_slot('kaptanto_%s')", sourceID))
		_, _ = c.Exec(context.Background(),
			fmt.Sprintf("DROP PUBLICATION IF EXISTS kaptanto_pub_%s", sourceID))
	})

	bin := buildBinary(t)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin,
		"--source", dsn,
		"--tables", "public."+table,
		"--output", "stdout",
		"--source-id", sourceID,
		"--data-dir", t.TempDir(),
		"--log-level", "warn",
	)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	// Collect events for our table off the NDJSON stream.
	type result struct {
		ops  []event.Operation
		keys []string
	}
	got := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var r result
		for scanner.Scan() {
			var ev event.ChangeEvent
			if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
				continue // skip non-event lines defensively
			}
			// Only care about DML on our table; ignore snapshot reads/controls.
			switch ev.Operation {
			case event.OpInsert, event.OpUpdate, event.OpDelete:
				r.ops = append(r.ops, ev.Operation)
				r.keys = append(r.keys, string(ev.Key))
				if len(r.ops) == 3 {
					got <- r
					return
				}
			}
		}
	}()

	// Let the replication slot get created and streaming start before writing.
	time.Sleep(3 * time.Second)
	_, err = conn.Exec(ctx, fmt.Sprintf("INSERT INTO public.%s (id, status) VALUES (1, 'new')", table))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, fmt.Sprintf("UPDATE public.%s SET status='done' WHERE id=1", table))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, fmt.Sprintf("DELETE FROM public.%s WHERE id=1", table))
	require.NoError(t, err)

	select {
	case r := <-got:
		require.Equal(t, []event.Operation{event.OpInsert, event.OpUpdate, event.OpDelete}, r.ops,
			"events must arrive in insert→update→delete order (RTR-04 per-key ordering)")
		require.Equal(t, r.keys[0], r.keys[1], "all events share the same primary key")
		require.Equal(t, r.keys[1], r.keys[2], "all events share the same primary key")
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for insert/update/delete events on stdout stream")
	}
}
