package dlq

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite_NilPayloadAndZeroCreatedAt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	err = s.Write(context.Background(), Entry{
		ID:         "nil-payload",
		ConsumerID: "c",
		EventID:    "e",
		Table:      "t",
		Payload:    nil,
		// CreatedAt zero → stamped now
	})
	require.NoError(t, err)
	got, err := s.Get(context.Background(), "nil-payload")
	require.NoError(t, err)
	assert.Empty(t, got.Payload)
	assert.False(t, got.CreatedAt.IsZero())
	assert.WithinDuration(t, time.Now().UTC(), got.CreatedAt, 5*time.Second)
}

func TestList_Empty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.List(context.Background(), Filter{})
	require.NoError(t, err)
	assert.Equal(t, []Entry{}, got)
}

func TestPurge_ByTableAndAll(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	require.NoError(t, s.Write(ctx, Entry{ID: "1", ConsumerID: "c", EventID: "e1", Table: "orders", Payload: []byte("{}")}))
	require.NoError(t, s.Write(ctx, Entry{ID: "2", ConsumerID: "c", EventID: "e2", Table: "users", Payload: []byte("{}")}))
	n, err := s.Purge(ctx, Filter{Table: "orders"})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = s.Purge(ctx, Filter{})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestStore_ErrorsAfterClose(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	ctx := context.Background()

	require.Error(t, s.Write(ctx, Entry{ConsumerID: "c", EventID: "e", Table: "t", Payload: []byte("{}")}))
	_, err = s.List(ctx, Filter{})
	require.Error(t, err)
	_, err = s.Get(ctx, "x")
	require.Error(t, err)
	require.Error(t, s.Delete(ctx, "x"))
	_, err = s.Purge(ctx, Filter{})
	require.Error(t, err)
	_ = s.Close() // second Close is best-effort; driver may return nil
}

func TestTruncateReason_Short(t *testing.T) {
	assert.Equal(t, "ok", truncateReason("ok"))
	assert.Equal(t, "", truncateReason(""))
}
