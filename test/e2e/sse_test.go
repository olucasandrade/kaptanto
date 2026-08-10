//go:build e2e

// TestE2E_Postgres_SSEOutput gives the SSE output path (the second, network
// output — gRPC gets in-process bufconn coverage at the unit level) black-box
// coverage: the same CRUD sequence must arrive over /events, an
// unauthenticated request must be rejected, and the `tables` query-param
// filter must actually exclude events for other tables.
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

func TestE2E_Postgres_SSEOutput(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (logical-replication Postgres) to run e2e tests")
	}

	fx := setupE2ETable(t, dsn, "id int PRIMARY KEY, status text")
	ctx := context.Background()

	const authToken = "e2e-sse-test-token-do-not-use-in-prod" //nolint:gosec // test-only fixture value
	port := freePort(t)

	bin := buildBinary(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin,
		"--source", dsn,
		"--tables", "public."+fx.Table,
		"--output", "sse",
		"--port", strconv.Itoa(port),
		"--auth-token", authToken,
		// No TLS cert configured for this test; --insecure opts out of the
		// startup TLS requirement for network outputs. The auth-token check
		// (independent of TLS) is what this test actually exercises.
		"--insecure",
		"--source-id", fx.SourceID,
		"--data-dir", t.TempDir(),
		"--log-level", "warn",
	)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHTTPUp(t, baseURL+"/healthz", authToken, 20*time.Second)

	// ---- Unauthenticated request must be rejected ----
	resp, err := http.Get(baseURL + "/events") //nolint:noctx // one-shot rejection check
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "unauthenticated /events must be rejected")

	// ---- Authenticated SSE stream, filtered to our table ----
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/events?tables="+fx.Table, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+authToken)
	sseResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sseResp.StatusCode)
	t.Cleanup(func() { _ = sseResp.Body.Close() })

	c := newEventCollector()
	tailSSE(sseResp.Body, c, fx.Table, dmlOps)

	// Give the SSE registration (router.Register, which loads the consumer's
	// cursor) time to land before writing, avoiding a subscribe-after-publish
	// race on the very first event.
	time.Sleep(1 * time.Second)
	_, err = fx.Conn.Exec(ctx, fmt.Sprintf("INSERT INTO public.%s (id, status) VALUES (1, 'new')", fx.Table))
	require.NoError(t, err)
	_, err = fx.Conn.Exec(ctx, fmt.Sprintf("UPDATE public.%s SET status='done' WHERE id=1", fx.Table))
	require.NoError(t, err)
	_, err = fx.Conn.Exec(ctx, fmt.Sprintf("DELETE FROM public.%s WHERE id=1", fx.Table))
	require.NoError(t, err)

	waitQuiescent(t, c, 3, 2*time.Second, 30*time.Second)
	events := c.snapshot()
	require.Len(t, events, 3, "expected exactly one insert/update/delete over SSE")
	require.Equal(t, event.OpInsert, events[0].Operation)
	require.Equal(t, event.OpUpdate, events[1].Operation)
	require.Equal(t, event.OpDelete, events[2].Operation)
	require.Equal(t, "new", decodeAfter(t, events[0]).Status, "insert payload")
	require.Equal(t, "done", decodeAfter(t, events[1]).Status, "update payload")
	// Default REPLICA IDENTITY sends only PK columns in an UPDATE/DELETE
	// before-image (see the WARN log kaptanto emits for this table) — Status
	// is genuinely absent, not a decode bug. This still proves the delete's
	// Before payload decodes successfully and carries the shape Postgres
	// actually sends.
	require.Empty(t, decodeBefore(t, events[2]).Status,
		"delete before-image under default REPLICA IDENTITY contains only PK columns, not status")
	require.Equal(t, 1, decodeKey(t, events[2]), "delete must carry the key")

	// ---- tables= filter: a stream subscribed to an unrelated table name
	// must not receive events for our table. ----
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/events?tables=does_not_exist", nil)
	require.NoError(t, err)
	req2.Header.Set("Authorization", "Bearer "+authToken)
	filteredResp, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, filteredResp.StatusCode)
	t.Cleanup(func() { _ = filteredResp.Body.Close() })

	fc := newEventCollector()
	tailSSE(filteredResp.Body, fc, "", nil)

	_, err = fx.Conn.Exec(ctx, fmt.Sprintf("INSERT INTO public.%s (id, status) VALUES (2, 'filtered-out')", fx.Table))
	require.NoError(t, err)

	// There is nothing to wait "until quiescent" for here — we expect zero
	// events. Poll the collector to prove the negative instead of a fixed sleep.
	waitNoEvents(t, fc, 3*time.Second)
	require.Empty(t, fc.snapshot(), "tables= filter must exclude events for a non-matching table")
}
