//go:build e2e

// Shared helpers for the e2e suite: an NDJSON/SSE event collector with
// quiescence-based waiting (no fixed sleeps for "is the stream done yet"),
// a per-test Postgres table fixture with slot/publication cleanup, and small
// payload-decoding helpers used across the CRUD, crash/restart, backfill, and
// SSE tests.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

// dmlOps is the operation allow-list used by tests that only care about
// insert/update/delete (i.e. not backfill "read" events or "control" signals).
var dmlOps = map[event.Operation]bool{
	event.OpInsert: true,
	event.OpUpdate: true,
	event.OpDelete: true,
}

// ─── event collection ────────────────────────────────────────────────────────

// eventCollector accumulates ChangeEvents read off a live process/HTTP stream
// on a background goroutine. It is safe for concurrent use: the tailing
// goroutine appends via add/finish while the test goroutine polls snapshots
// via snapshot/count/since to decide when the stream has gone quiet.
type eventCollector struct {
	mu     sync.Mutex
	events []event.ChangeEvent
	lastAt time.Time
	done   chan struct{}
	err    error
}

func newEventCollector() *eventCollector {
	return &eventCollector{done: make(chan struct{})}
}

func (c *eventCollector) add(ev event.ChangeEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.lastAt = time.Now()
	c.mu.Unlock()
}

// finish marks the tailing goroutine as done (stream EOF'd or errored) and
// closes done so waiters relying on stream termination wake up immediately.
func (c *eventCollector) finish(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

func (c *eventCollector) snapshot() []event.ChangeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]event.ChangeEvent, len(c.events))
	copy(out, c.events)
	return out
}

func (c *eventCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// since returns how long it has been since the last event arrived. Zero
// (never arrived) reports 0, not a huge duration, so quiescence checks that
// also require a minimum count don't fire before anything has been seen.
func (c *eventCollector) since() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastAt.IsZero() {
		return 0
	}
	return time.Since(c.lastAt)
}

// tailNDJSON starts a background goroutine reading newline-delimited
// ChangeEvent JSON from r (the kaptanto binary's stdout) into c. table
// restricts collection to one table ("" = all); ops restricts to a set of
// operations (nil = all).
func tailNDJSON(r io.Reader, c *eventCollector, table string, ops map[event.Operation]bool) {
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			var ev event.ChangeEvent
			if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
				continue // skip non-event lines defensively
			}
			if table != "" && ev.Table != table {
				continue
			}
			if ops != nil && !ops[ev.Operation] {
				continue
			}
			c.add(ev)
		}
		c.finish(scanner.Err())
	}()
}

// tailSSE starts a background goroutine reading the SSE wire format
// ("id: <ULID>\ndata: <json>\n\n", per internal/output/sse/consumer.go) from
// r into c, applying the same table/ops filtering as tailNDJSON.
func tailSSE(r io.Reader, c *eventCollector, table string, ops map[event.Operation]bool) {
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue // "id: ...", blank terminator, or ": ping" keepalive comment
			}
			var ev event.ChangeEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if table != "" && ev.Table != table {
				continue
			}
			if ops != nil && !ops[ev.Operation] {
				continue
			}
			c.add(ev)
		}
		c.finish(scanner.Err())
	}()
}

// waitForCount blocks until c has collected at least n events, the collector
// finishes (stream EOF), or timeout elapses. It polls on a short, fixed tick
// only to check real, already-observed state (event count); the actual "are
// we done" decision reacts to that state, not to elapsed wall-clock time.
func waitForCount(t *testing.T, c *eventCollector, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if c.count() >= n {
			return
		}
		select {
		case <-c.done:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Logf("waitForCount: timed out after %s waiting for %d events; got %d", timeout, n, c.count())
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitQuiescent blocks until c has collected at least minCount events AND no
// new event has arrived for `quiet`, or until the collector finishes (stream
// EOF), or until hardTimeout elapses as a safety net. This is the "wait until
// quiescent" pattern used instead of a fixed sleep: the stop condition is
// driven by actual event arrival timestamps, so a slow CI runner naturally
// gets more wall-clock time rather than the test flaking on a too-short sleep.
func waitQuiescent(t *testing.T, c *eventCollector, minCount int, quiet, hardTimeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(hardTimeout)
	for {
		select {
		case <-c.done:
			return
		default:
		}
		if c.count() >= minCount && c.since() >= quiet {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("waitQuiescent: hard timeout after %s; collected %d events", hardTimeout, c.count())
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// ─── Postgres table fixture ──────────────────────────────────────────────────

// suffixCounter guarantees uniqueness even when two tests grab a suffix
// within the same millisecond.
var suffixCounter atomic.Int64

func uniqueSuffix() string {
	return fmt.Sprintf("%s%03d", time.Now().Format("150405000"), suffixCounter.Add(1)%1000)
}

// e2eFixture bundles a live Postgres connection with a unique table/source-id
// pair for one e2e test. Cleanup (table drop, slot drop, publication drop) is
// registered automatically via t.Cleanup.
type e2eFixture struct {
	Conn     *pgx.Conn
	Table    string
	SourceID string
}

// setupE2ETable connects to dsn, creates public.<unique>(columns), and
// registers best-effort cleanup of the table, replication slot, and
// publication kaptanto creates (kaptanto_<sourceID> / kaptanto_pub_<sourceID>).
func setupE2ETable(t *testing.T, dsn, columns string) *e2eFixture {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	suffix := uniqueSuffix()
	table := "e2e_" + suffix
	sourceID := "e2e_" + suffix

	_, err = conn.Exec(ctx, fmt.Sprintf("CREATE TABLE public.%s (%s)", table, columns))
	require.NoError(t, err)
	// Deliberately NOT setting REPLICA IDENTITY FULL: pgoutput reuses the
	// same per-column "flags" bit for both "is this column part of the
	// PRIMARY KEY" and "is this column part of REPLICA IDENTITY" — under
	// FULL, every column gets that bit, which would make extractPK
	// (internal/parser/pgoutput/types.go) treat the whole row as the CDC
	// key instead of just the PK. That's a real, separate parser gap
	// (extractPK's doc comment only accounts for the DEFAULT/PK case) —
	// out of scope for this test fix. Tests must live with the default
	// REPLICA IDENTITY's PK-only before-image (see decodeBefore usage).

	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS public.%s", table))
		_, _ = c.Exec(context.Background(),
			fmt.Sprintf("SELECT pg_drop_replication_slot('kaptanto_%s')", sourceID))
		_, _ = c.Exec(context.Background(),
			fmt.Sprintf("DROP PUBLICATION IF EXISTS kaptanto_pub_%s", sourceID))
	})

	return &e2eFixture{Conn: conn, Table: table, SourceID: sourceID}
}

// ─── payload decoding ─────────────────────────────────────────────────────────

// rowPayload deliberately omits "id": the WAL streaming path (pgoutput's
// text wire format, decodeColumns in internal/parser/pgoutput/types.go)
// always emits it as a JSON string ("1"), while the backfill/snapshot path
// (internal/backfill/backfill.go, which uses the row's native pgx value
// straight from rows.Values()) emits it as a JSON number (1) — a real,
// pre-existing inconsistency between the two paths, not something these
// tests are set up to assert on. json.Unmarshal silently ignores JSON keys
// with no matching struct field, so decoding both shapes just works as long
// as ID isn't declared here. Only Status is ever asserted on by these tests.
type rowPayload struct {
	Status string `json:"status"`
}

func decodeAfter(t *testing.T, ev event.ChangeEvent) rowPayload {
	t.Helper()
	var p rowPayload
	require.NoErrorf(t, json.Unmarshal(ev.After, &p), "decode After payload for %s %s", ev.Operation, ev.IdempotencyKey)
	return p
}

func decodeBefore(t *testing.T, ev event.ChangeEvent) rowPayload {
	t.Helper()
	var p rowPayload
	require.NoErrorf(t, json.Unmarshal(ev.Before, &p), "decode Before payload for %s %s", ev.Operation, ev.IdempotencyKey)
	return p
}

// decodeKey decodes ev.Key's "id" field and returns it as an int for callers
// that use it as a map key / loop counter. The wire value itself is a JSON
// string, not a number: primary keys go through pk.Canonical (internal/pk),
// which converts every PK value to its canonical decimal-string form (e.g.
// {"id":"2"}, not {"id":2}) so the WAL and snapshot paths byte-match — see
// pk.Canonical's doc comment. strconv.Atoi converts back to the int these
// tests were written against; every table this suite creates uses a plain
// integer "id" primary key, so the conversion always succeeds.
func decodeKey(t *testing.T, ev event.ChangeEvent) int {
	t.Helper()
	var k struct {
		ID string `json:"id"`
	}
	require.NoErrorf(t, json.Unmarshal(ev.Key, &k), "decode Key for %s %s", ev.Operation, ev.IdempotencyKey)
	id, err := strconv.Atoi(k.ID)
	require.NoErrorf(t, err, "decoded key %q for %s %s is not an integer", k.ID, ev.Operation, ev.IdempotencyKey)
	return id
}

// ─── HTTP helpers (SSE test) ─────────────────────────────────────────────────

// freePort asks the OS for an unused TCP port so parallel/repeat test runs
// don't collide on a hardcoded port number.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForHTTPUp polls url until it returns a non-5xx response or timeout
// elapses. Used to detect that the SSE server's HTTP listener is accepting
// connections before the test issues real requests against it.
func waitForHTTPUp(t *testing.T, url, bearerToken string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err == nil {
			if bearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+bearerToken)
			}
			resp, doErr := client.Do(req)
			if doErr == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %s", url, timeout)
}
