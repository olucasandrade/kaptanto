//go:build e2e

// TestE2E_Postgres_BackfillDuringStream exercises BKF-02 black-box: backfill
// (which every table config runs on first start — see buildBackfillConfigs
// in internal/cmd/filters.go, strategy is always "snapshot_and_stream") must
// cover every pre-existing row, and a snapshot row superseded by a concurrent
// WAL update must not overwrite the fresher WAL data in the emitted stream.
// Before this test, backfill-during-streaming had no black-box coverage.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

func TestE2E_Postgres_BackfillDuringStream(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (logical-replication Postgres) to run e2e tests")
	}

	fx := setupE2ETable(t, dsn, "id int PRIMARY KEY, status text")
	ctx := context.Background()

	const totalRows = 1500
	const racedRows = 30 // highest `racedRows` ids get updated concurrently with the snapshot

	// Pre-populate BEFORE starting kaptanto: these rows predate the
	// replication slot, so the only way they can reach the stream is the
	// backfill snapshot, not WAL.
	_, err := fx.Conn.Exec(ctx, fmt.Sprintf(
		`INSERT INTO public.%s (id, status) SELECT g, 'snapshot-v1' FROM generate_series(1, %d) AS g`,
		fx.Table, totalRows))
	require.NoError(t, err)

	// Dedicated connection for the racer so each UPDATE is a fast round trip
	// on an already-open connection (a fresh connection per statement would
	// slow the racer down and shrink its chance of beating the snapshot).
	racerConn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = racerConn.Close(context.Background()) })

	bin := buildBinary(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin,
		"--source", dsn,
		"--tables", "public."+fx.Table,
		"--output", "stdout",
		"--source-id", fx.SourceID,
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

	c := newEventCollector()
	// Capture both read (snapshot) and update (WAL) events for our table;
	// nothing else is relevant to the watermark check.
	tailNDJSON(stdout, c, fx.Table, map[event.Operation]bool{
		event.OpRead:   true,
		event.OpUpdate: true,
	})

	// Wait until the replication slot exists so WAL updates from the racer are
	// retained — an update predating the slot never streams (see comment below).
	waitForReplicationSlot(t, fx.Conn, fx.SourceID, 30*time.Second)

	// Race a batch of concurrent updates against the snapshot: backfill's
	// SnapshotLSN is pg_current_wal_flush_lsn() at process start (see
	// BackfillEngineImpl.Run), so every update issued from here on has a
	// commit LSN newer than the snapshot's watermark — the only open
	// question the watermark check resolves is whether each update's WAL
	// event reaches the EventLog before the snapshot's row-by-row scan
	// (which walks ascending PK order) reaches that same row. Targeting the
	// highest ids first — the last rows the ascending scan will visit —
	// maximizes that chance.
	//
	// A brief delay before the first update gives kaptanto time to create
	// its replication slot/publication first: an update racer starts
	// firing before the slot exists, the resulting WAL record predates the
	// slot's start LSN and Postgres never streams it at all — a hard
	// requirement below is "the update must always arrive" (unlike the
	// read, which the watermark check is allowed to discard), so this
	// isn't optional.
	go func() {
		for id := totalRows; id > totalRows-racedRows; id-- {
			_, _ = racerConn.Exec(context.Background(), fmt.Sprintf(
				"UPDATE public.%s SET status='updated-during-snapshot' WHERE id=$1", fx.Table), id)
		}
	}()

	// Expect at least totalRows events (every row shows up as either a read,
	// an update, or — for a row whose read arrived before its racing update —
	// both). Bound the hard timeout generously: building the binary already
	// happened, but the snapshot itself must walk 1500 rows in ~6 batches.
	waitQuiescent(t, c, totalRows, 4*time.Second, 90*time.Second)
	events := c.snapshot()
	require.NotEmpty(t, events, "no read or update events observed")

	reads := make(map[int]event.ChangeEvent)
	updates := make(map[int]event.ChangeEvent)
	readIdx := make(map[int]int)
	updateIdx := make(map[int]int)
	for i, ev := range events {
		id := decodeKey(t, ev)
		switch ev.Operation {
		case event.OpRead:
			reads[id] = ev
			readIdx[id] = i
		case event.OpUpdate:
			updates[id] = ev
			updateIdx[id] = i
		}
	}

	// Rows outside the race window must each arrive as exactly one snapshot
	// read with the original data — straightforward backfill completeness.
	for id := 1; id <= totalRows-racedRows; id++ {
		ev, ok := reads[id]
		require.Truef(t, ok, "row id=%d (outside the race window) never arrived as a snapshot read", id)
		require.Equalf(t, "snapshot-v1", decodeAfter(t, ev).Status,
			"row id=%d snapshot read carries unexpected payload", id)
		_, hasUpdate := updates[id]
		require.Falsef(t, hasUpdate, "row id=%d (outside the race window) unexpectedly has an update event", id)
	}

	// Raced rows: the update must always arrive (it's a normal WAL change,
	// unrelated to the snapshot). The read may or may not arrive — the
	// watermark check is allowed to discard it once the row is superseded —
	// but if it DOES arrive, it must not land after the update in the
	// emitted stream. That would mean the watermark check saw the newer WAL
	// event already in the EventLog and still let the stale snapshot row
	// through, overwriting fresher data downstream (the BKF-02 violation
	// this test exists to catch).
	discardedReads := 0
	for id := totalRows - racedRows + 1; id <= totalRows; id++ {
		upEv, hasUpdate := updates[id]
		require.Truef(t, hasUpdate, "raced row id=%d: the concurrent update was never observed in the stream", id)
		require.Equalf(t, "updated-during-snapshot", decodeAfter(t, upEv).Status,
			"raced row id=%d update payload mismatch", id)

		readEv, hasRead := reads[id]
		if !hasRead {
			discardedReads++
			continue
		}
		require.Equalf(t, "snapshot-v1", decodeAfter(t, readEv).Status,
			"raced row id=%d snapshot read carries unexpected payload", id)
		require.Lessf(t, readIdx[id], updateIdx[id],
			"raced row id=%d: snapshot read arrived AFTER its concurrent update in the "+
				"emitted stream — BKF-02's watermark check should have discarded this stale "+
				"read once the newer WAL event was in the EventLog, but it was delivered "+
				"anyway, overwriting fresher data downstream", id)
	}
	t.Logf("backfill/stream race: %d/%d raced rows had their stale snapshot read discarded by the watermark check",
		discardedReads, racedRows)
}
