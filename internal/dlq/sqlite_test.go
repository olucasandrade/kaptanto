package dlq_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openSQLiteStore(t *testing.T) dlq.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dlq.db")
	s, err := dlq.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStoreContract(t *testing.T) {
	runStoreContract(t, openSQLiteStore)
}

func TestSQLiteStore_OpenCreatesSchema(t *testing.T) {
	s, err := dlq.Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
}

func TestSQLiteStore_OpenInvalidPath(t *testing.T) {
	_, err := dlq.Open(filepath.Join(t.TempDir(), "no-such-dir", "dlq.db"))
	require.Error(t, err)
}

func TestSQLiteStore_AfterClose(t *testing.T) {
	s, err := dlq.Open(filepath.Join(t.TempDir(), "dlq.db"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	err = s.Write(context.Background(), dlq.Entry{
		ConsumerID: "c", EventID: "e", Table: "t", Payload: []byte("{}"),
	})
	require.Error(t, err)
}

func TestSQLiteStore_ConcurrentTwoHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	a, err := dlq.Open(path)
	require.NoError(t, err)
	defer func() { _ = a.Close() }()
	b, err := dlq.Open(path)
	require.NoError(t, err)
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 64)

	for i := 0; i < 32; i++ {
		wg.Add(2)
		i := i
		go func() {
			defer wg.Done()
			e := sampleEntry("writer-a", "evt-a-"+itoa(i), "t", 0, uint64(i))
			errCh <- a.Write(ctx, e)
		}()
		go func() {
			defer wg.Done()
			e := sampleEntry("writer-b", "evt-b-"+itoa(i), "t", 1, uint64(i))
			errCh <- b.Write(ctx, e)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err, "concurrent WAL write must not return SQLITE_BUSY")
	}

	// Cross-handle visibility: b lists what a wrote.
	got, err := b.List(ctx, dlq.Filter{ConsumerID: "writer-a"})
	require.NoError(t, err)
	assert.Len(t, got, 32)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b[pos:])
}

func BenchmarkWrite(b *testing.B) {
	path := filepath.Join(b.TempDir(), "dlq.db")
	s, err := dlq.Open(path)
	require.NoError(b, err)
	b.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	payload := []byte("{\"op\":\"insert\"}")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := dlq.Entry{
			ConsumerID:     "bench",
			EventID:        "evt-" + itoa(i),
			Table:          "public.orders",
			PartitionID:    uint32(i % 64),
			Seq:            uint64(i + 1),
			Attempts:       5,
			Reason:         "timeout",
			IdempotencyKey: "ikey-" + itoa(i),
			Payload:        payload,
		}
		if err := s.Write(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}
