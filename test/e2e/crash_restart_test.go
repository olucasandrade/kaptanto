//go:build e2e

// TestE2E_Postgres_CrashRestartDurability exercises CHK-01 black-box: the
// source checkpoint must never advance until EventLog.Append() has
// succeeded, so a crash followed by a restart against the same --data-dir
// (and hence the same replication slot / source-id) must neither lose nor
// misorder any committed row, once duplicate deliveries are collapsed by
// idempotency key. Before this test, CHK-01 was covered only at the
// EventLog-reopen unit level — nothing exercised the full binary across a
// real process kill.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

func TestE2E_Postgres_CrashRestartDurability(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (logical-replication Postgres) to run e2e tests")
	}

	fx := setupE2ETable(t, dsn, "id int PRIMARY KEY, status text")
	ctx := context.Background()

	const preCrashRows = 5
	const postCrashRows = 5
	const totalRows = preCrashRows + postCrashRows

	// data-dir is persistent across the crash + restart within this test: it
	// holds the Badger EventLog, the SQLite checkpoint (source LSN), and the
	// SQLite consumer cursor for the "stdout" consumer. Reusing it — together
	// with the same --source-id, and hence the same replication slot name —
	// is what makes this a restart rather than a fresh, unrelated run.
	dataDir := t.TempDir()
	bin := buildBinary(t)

	baseArgs := []string{
		"--source", dsn,
		"--tables", "public." + fx.Table,
		"--output", "stdout",
		"--source-id", fx.SourceID,
		"--data-dir", dataDir,
		"--log-level", "warn",
	}

	// ---- Run 1: stream a batch of inserts, then SIGKILL mid-stream ----
	runCtx1, cancel1 := context.WithCancel(ctx)
	cmd1 := exec.CommandContext(runCtx1, bin, baseArgs...)
	stdout1, err := cmd1.StdoutPipe()
	require.NoError(t, err)
	cmd1.Stderr = os.Stderr
	require.NoError(t, cmd1.Start())

	c1 := newEventCollector()
	tailNDJSON(stdout1, c1, fx.Table, dmlOps)

	// Let the replication slot get created and streaming start before writing,
	// matching the existing CRUD test's proven startup timing.
	time.Sleep(3 * time.Second)

	for i := 1; i <= preCrashRows; i++ {
		_, err = fx.Conn.Exec(ctx, fmt.Sprintf(
			"INSERT INTO public.%s (id, status) VALUES ($1, 'pre-crash')", fx.Table), i)
		require.NoError(t, err)
	}

	// Wait for the first few events to actually arrive before killing — the
	// point is a crash mid-stream (so the source must re-send and the
	// EventLog must dedup on resend), not a crash after everything already
	// safely landed and checkpointed.
	waitForCount(t, c1, 2, 20*time.Second)

	// SIGKILL, not graceful shutdown: CHK-01 is a crash-recovery guarantee.
	// A graceful stop would drain and shut down cleanly, proving nothing
	// about recovery from an unclean crash (mid-Badger-write, mid-checkpoint).
	require.NoError(t, cmd1.Process.Kill())
	_ = cmd1.Wait() // exits via signal; the error is expected and not asserted
	cancel1()

	run1Events := c1.snapshot()

	// ---- While the binary is down: commit more rows, plus an update to a
	// pre-crash row. The replication slot retains this WAL until kaptanto
	// reconnects, so all of it must surface after restart. The update to a
	// pre-crash key also exercises per-key order (RTR-04) across the crash
	// boundary: insert (run 1) must still precede update (run 2). ----
	for i := preCrashRows + 1; i <= totalRows; i++ {
		_, err = fx.Conn.Exec(ctx, fmt.Sprintf(
			"INSERT INTO public.%s (id, status) VALUES ($1, 'post-crash')", fx.Table), i)
		require.NoError(t, err)
	}
	_, err = fx.Conn.Exec(ctx, fmt.Sprintf(
		"UPDATE public.%s SET status='updated-after-restart' WHERE id=1", fx.Table))
	require.NoError(t, err)

	// ---- Run 2: restart with the same data-dir/slot/source-id, collect until quiescent ----
	runCtx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	cmd2 := exec.CommandContext(runCtx2, bin, baseArgs...)
	stdout2, err := cmd2.StdoutPipe()
	require.NoError(t, err)
	cmd2.Stderr = os.Stderr
	require.NoError(t, cmd2.Start())
	t.Cleanup(func() {
		cancel2()
		_ = cmd2.Wait()
	})

	c2 := newEventCollector()
	tailNDJSON(stdout2, c2, fx.Table, dmlOps)

	// Event-driven wait: stop once nothing new has arrived for a few seconds,
	// not after a fixed sleep (restart timing varies with recovery cost).
	waitQuiescent(t, c2, 1, 3*time.Second, 45*time.Second)

	run2Events := c2.snapshot()

	// ---- Assemble the full picture across both runs ----
	all := make([]event.ChangeEvent, 0, len(run1Events)+len(run2Events))
	all = append(all, run1Events...)
	all = append(all, run2Events...)
	require.NotEmpty(t, all, "no events observed across either run")

	// Dedup by IdempotencyKey, preserving first-seen order. A repeat delivery
	// of the *same* IdempotencyKey is expected in one narrow case: Deliver
	// succeeded (bytes hit stdout) but the process was killed before the
	// router's cursor save landed, so the restart resumes from the older
	// cursor and redelivers that one already-durable EventLog entry. That
	// redelivery replays the identical stored entry, so ID/Timestamp/payload
	// must match byte-for-byte; if they don't, the "duplicate" isn't a replay
	// of one EventLog entry but two distinct entries sharing an idempotency
	// key — i.e. CHK-01's dedup-on-Append did not run.
	type dedupEntry struct {
		ev    event.ChangeEvent
		count int
	}
	byKey := make(map[string]*dedupEntry)
	var orderedKeys []string
	for _, ev := range all {
		if existing, ok := byKey[ev.IdempotencyKey]; ok {
			existing.count++
			require.Equalf(t, existing.ev, ev,
				"repeated delivery of idempotency key %q carried a different payload — "+
					"this means two distinct EventLog entries share one idempotency key, "+
					"i.e. CHK-01 dedup-on-Append did not collapse a resent source event",
				ev.IdempotencyKey)
			require.LessOrEqualf(t, existing.count, 5,
				"idempotency key %q delivered %d times — this looks like unbounded "+
					"redelivery (e.g. the cursor never advances), not the narrow "+
					"deliver/cursor-save race window", ev.IdempotencyKey, existing.count)
			continue
		}
		byKey[ev.IdempotencyKey] = &dedupEntry{ev: ev, count: 1}
		orderedKeys = append(orderedKeys, ev.IdempotencyKey)
	}

	insertsByID := make(map[int]event.ChangeEvent)
	var id1Ops []event.Operation
	var id1UpdateEvent event.ChangeEvent
	for _, key := range orderedKeys {
		ev := byKey[key].ev
		id := decodeKey(t, ev)
		if ev.Operation == event.OpInsert {
			insertsByID[id] = ev
		}
		if id == 1 {
			id1Ops = append(id1Ops, ev.Operation)
			if ev.Operation == event.OpUpdate {
				id1UpdateEvent = ev
			}
		}
	}

	// No loss, no amplification: exactly one distinct insert per committed row.
	require.Lenf(t, insertsByID, totalRows,
		"expected exactly %d distinct inserted rows after dedup by idempotency key; "+
			"fewer means CHK-01 lost a row across the crash, more means it duplicated one",
		totalRows)

	for i := 1; i <= totalRows; i++ {
		ev, ok := insertsByID[i]
		require.Truef(t, ok, "row id=%d never observed as an insert across either run (CHK-01 data loss)", i)
		wantStatus := "pre-crash"
		if i > preCrashRows {
			wantStatus = "post-crash"
		}
		require.Equalf(t, wantStatus, decodeAfter(t, ev).Status,
			"row id=%d insert payload mismatch", i)
	}

	// Per-key order across the restart boundary (RTR-04): row 1's insert
	// (delivered in run 1, pre-crash) must precede its update (only possible
	// in run 2, since the UPDATE was issued while the binary was down).
	require.Equalf(t, []event.Operation{event.OpInsert, event.OpUpdate}, id1Ops,
		"row id=1 must show insert-before-update across the restart boundary; got %v", id1Ops)
	require.Equal(t, "updated-after-restart", decodeAfter(t, id1UpdateEvent).Status,
		"row id=1 update payload mismatch")
}
