package dlq_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openStore is a factory that returns a fresh Store for contract tests.
// Implementations (SQLite, NDJSON, cascade) pass their opener here.
type openStore func(t *testing.T) dlq.Store

// runStoreContract exercises the Store interface. NDJSON/cascade stores reuse this.
func runStoreContract(t *testing.T, open openStore) {
	t.Helper()
	t.Run("WriteAndGet", func(t *testing.T) { contractWriteAndGet(t, open) })
	t.Run("DLQ02_IdempotentWrite", func(t *testing.T) { contractIdempotentWrite(t, open) })
	t.Run("ListOrdering", func(t *testing.T) { contractListOrdering(t, open) })
	t.Run("ListFilters", func(t *testing.T) { contractListFilters(t, open) })
	t.Run("GetNotFound", func(t *testing.T) { contractGetNotFound(t, open) })
	t.Run("Delete", func(t *testing.T) { contractDelete(t, open) })
	t.Run("Purge", func(t *testing.T) { contractPurge(t, open) })
	t.Run("ReasonTruncation", func(t *testing.T) { contractReasonTruncation(t, open) })
	t.Run("MintULID", func(t *testing.T) { contractMintULID(t, open) })
}

func sampleEntry(consumer, eventID, table string, part uint32, seq uint64) dlq.Entry {
	return dlq.Entry{
		ConsumerID:     consumer,
		EventID:        eventID,
		Table:          table,
		PartitionID:    part,
		Seq:            seq,
		Attempts:       3,
		Reason:         "delivery failed",
		IdempotencyKey: "ikey-" + eventID,
		Payload:        []byte("{\"id\":\"" + eventID + "\"}"),
		CreatedAt:      time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
}

func contractWriteAndGet(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	in := sampleEntry("c1", "evt-1", "public.orders", 2, 10)
	in.ID = "01HXYZ00000000000000000001"
	require.NoError(t, s.Write(ctx, in))

	got, err := s.Get(ctx, in.ID)
	require.NoError(t, err)
	assert.Equal(t, in.ID, got.ID)
	assert.Equal(t, in.ConsumerID, got.ConsumerID)
	assert.Equal(t, in.EventID, got.EventID)
	assert.Equal(t, in.Table, got.Table)
	assert.Equal(t, in.PartitionID, got.PartitionID)
	assert.Equal(t, in.Seq, got.Seq)
	assert.Equal(t, in.Attempts, got.Attempts)
	assert.Equal(t, in.Reason, got.Reason)
	assert.Equal(t, in.IdempotencyKey, got.IdempotencyKey)
	assert.Equal(t, in.Payload, got.Payload)
	assert.Equal(t, in.CreatedAt.UnixMilli(), got.CreatedAt.UnixMilli())
}

func contractIdempotentWrite(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	e1 := sampleEntry("c1", "evt-dup", "public.orders", 1, 1)
	e1.ID = "01HXYZ00000000000000000002"
	e2 := e1
	e2.ID = "01HXYZ00000000000000000003" // different ID, same consumer+event
	e2.Reason = "second attempt reason"
	e2.Payload = []byte("{\"changed\":true}")

	require.NoError(t, s.Write(ctx, e1))
	require.NoError(t, s.Write(ctx, e2), "DLQ-02: conflicting Write must return nil")

	all, err := s.List(ctx, dlq.Filter{ConsumerID: "c1"})
	require.NoError(t, err)
	require.Len(t, all, 1, "DLQ-02: only one row for (consumer, event)")
	assert.Equal(t, e1.ID, all[0].ID, "first write wins")
	assert.Equal(t, e1.Reason, all[0].Reason)
}
func contractListOrdering(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	entries := []dlq.Entry{
		sampleEntry("b", "e1", "t", 2, 2),
		sampleEntry("a", "e2", "t", 1, 9),
		sampleEntry("a", "e3", "t", 1, 3),
		sampleEntry("a", "e4", "t", 0, 5),
		sampleEntry("b", "e5", "t", 2, 1),
	}
	for i := range entries {
		entries[i].ID = "id-order-" + entries[i].EventID
		require.NoError(t, s.Write(ctx, entries[i]))
	}

	got, err := s.List(ctx, dlq.Filter{})
	require.NoError(t, err)
	require.Len(t, got, 5)

	assert.Equal(t, "a", got[0].ConsumerID)
	assert.Equal(t, uint32(0), got[0].PartitionID)
	assert.Equal(t, uint64(5), got[0].Seq)

	assert.Equal(t, "a", got[1].ConsumerID)
	assert.Equal(t, uint32(1), got[1].PartitionID)
	assert.Equal(t, uint64(3), got[1].Seq)

	assert.Equal(t, "a", got[2].ConsumerID)
	assert.Equal(t, uint32(1), got[2].PartitionID)
	assert.Equal(t, uint64(9), got[2].Seq)

	assert.Equal(t, "b", got[3].ConsumerID)
	assert.Equal(t, uint32(2), got[3].PartitionID)
	assert.Equal(t, uint64(1), got[3].Seq)

	assert.Equal(t, "b", got[4].ConsumerID)
	assert.Equal(t, uint32(2), got[4].PartitionID)
	assert.Equal(t, uint64(2), got[4].Seq)
}

func contractListFilters(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()

	old := sampleEntry("c1", "old-1", "public.orders", 0, 1)
	old.ID = "id-old"
	old.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Write(ctx, old))

	mid := sampleEntry("c1", "mid-1", "public.users", 0, 2)
	mid.ID = "id-mid"
	mid.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Write(ctx, mid))

	other := sampleEntry("c2", "c2-1", "public.orders", 0, 3)
	other.ID = "id-other"
	other.CreatedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Write(ctx, other))

	byConsumer, err := s.List(ctx, dlq.Filter{ConsumerID: "c1"})
	require.NoError(t, err)
	require.Len(t, byConsumer, 2)

	byTable, err := s.List(ctx, dlq.Filter{Table: "public.orders"})
	require.NoError(t, err)
	require.Len(t, byTable, 2)
	for _, e := range byTable {
		assert.Equal(t, "public.orders", e.Table)
	}

	olderThan := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	byAge, err := s.List(ctx, dlq.Filter{OlderThan: olderThan})
	require.NoError(t, err)
	require.Len(t, byAge, 2) // old + other

	limited, err := s.List(ctx, dlq.Filter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, limited, 1)

	combo, err := s.List(ctx, dlq.Filter{
		ConsumerID: "c1",
		Table:      "public.orders",
		OlderThan:  olderThan,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, combo, 1)
	assert.Equal(t, "old-1", combo[0].EventID)
}
func contractGetNotFound(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	_, err := s.Get(context.Background(), "missing-id")
	require.Error(t, err)
	assert.True(t, errors.Is(err, dlq.ErrNotFound), "got %v", err)
}

func contractDelete(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	e1 := sampleEntry("c1", "d1", "t", 0, 1)
	e1.ID = "del-1"
	e2 := sampleEntry("c1", "d2", "t", 0, 2)
	e2.ID = "del-2"
	require.NoError(t, s.Write(ctx, e1))
	require.NoError(t, s.Write(ctx, e2))

	require.NoError(t, s.Delete(ctx)) // empty no-op
	require.NoError(t, s.Delete(ctx, "del-1", "missing"))

	_, err := s.Get(ctx, "del-1")
	assert.True(t, errors.Is(err, dlq.ErrNotFound))
	got, err := s.Get(ctx, "del-2")
	require.NoError(t, err)
	assert.Equal(t, "del-2", got.ID)
}

func contractPurge(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()

	old := sampleEntry("c1", "p-old", "public.orders", 0, 1)
	old.ID = "purge-old"
	old.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keep := sampleEntry("c1", "p-keep", "public.orders", 0, 2)
	keep.ID = "purge-keep"
	keep.CreatedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	other := sampleEntry("c2", "p-other", "public.orders", 0, 3)
	other.ID = "purge-other"
	other.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Write(ctx, old))
	require.NoError(t, s.Write(ctx, keep))
	require.NoError(t, s.Write(ctx, other))

	n, err := s.Purge(ctx, dlq.Filter{
		ConsumerID: "c1",
		OlderThan:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	all, err := s.List(ctx, dlq.Filter{})
	require.NoError(t, err)
	require.Len(t, all, 2)
	ids := map[string]bool{}
	for _, e := range all {
		ids[e.ID] = true
	}
	assert.True(t, ids["purge-keep"])
	assert.True(t, ids["purge-other"])
	assert.False(t, ids["purge-old"])
}

func contractReasonTruncation(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	e := sampleEntry("c1", "long-reason", "t", 0, 1)
	e.ID = "reason-1"
	e.Reason = strings.Repeat("x", 2000)
	require.NoError(t, s.Write(ctx, e))
	got, err := s.Get(ctx, e.ID)
	require.NoError(t, err)
	assert.Len(t, got.Reason, 1024)
}

func contractMintULID(t *testing.T, open openStore) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	e := sampleEntry("c1", "mint-1", "t", 0, 1)
	e.ID = "" // force mint
	require.NoError(t, s.Write(ctx, e))
	all, err := s.List(ctx, dlq.Filter{ConsumerID: "c1"})
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Len(t, all[0].ID, 26, "ULID string should be 26 chars")
}
