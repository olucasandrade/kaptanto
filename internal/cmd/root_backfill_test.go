// Package cmd provides TDD tests for backfill wiring helpers and integration
// tests exercising BackfillEngineImpl directly.
//
// This file is in package cmd (not cmd_test) so it can access unexported
// functions like buildBackfillConfigs.
package cmd

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Task 1: buildBackfillConfigs helper ---

// TestBuildBackfillConfigs verifies the four behaviour cases.
func TestBuildBackfillConfigs(t *testing.T) {
	t.Run("nil tables returns empty slice without panic", func(t *testing.T) {
		result := buildBackfillConfigs(nil, "default")
		require.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("single schema-qualified table", func(t *testing.T) {
		tables := map[string]config.TableConfig{
			"public.orders": {},
		}
		result := buildBackfillConfigs(tables, "default")
		require.Len(t, result, 1)
		cfg := result[0]
		assert.Equal(t, "default", cfg.SourceID)
		assert.Equal(t, "public", cfg.Schema)
		assert.Equal(t, "orders", cfg.Table)
		assert.Equal(t, "snapshot_and_stream", cfg.Strategy)
		assert.Equal(t, []string{"id"}, cfg.PKCols)
		assert.Equal(t, uint32(numEventLogPartitions), cfg.NumPartitions)
	})

	t.Run("unqualified table no schema prefix", func(t *testing.T) {
		tables := map[string]config.TableConfig{
			"orders": {},
		}
		result := buildBackfillConfigs(tables, "default")
		require.Len(t, result, 1)
		cfg := result[0]
		assert.Equal(t, "", cfg.Schema)
		assert.Equal(t, "orders", cfg.Table)
	})

	t.Run("two table entries returns two configs", func(t *testing.T) {
		tables := map[string]config.TableConfig{
			"public.orders": {},
			"public.users":  {},
		}
		result := buildBackfillConfigs(tables, "default")
		assert.Len(t, result, 2)
	})
}

// --- Task 2: BackfillEngineImpl integration tests ---

// mockBackfillStore is a minimal in-memory BackfillStore for tests.
type mockBackfillStore struct {
	states map[string]*backfill.BackfillState
}

func newMockBackfillStore() *mockBackfillStore {
	return &mockBackfillStore{states: make(map[string]*backfill.BackfillState)}
}

func (m *mockBackfillStore) SaveState(_ context.Context, state *backfill.BackfillState) error {
	key := state.SourceID + "/" + state.Table
	cp := *state
	m.states[key] = &cp
	return nil
}

func (m *mockBackfillStore) LoadState(_ context.Context, sourceID, table string) (*backfill.BackfillState, error) {
	key := sourceID + "/" + table
	s, ok := m.states[key]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (m *mockBackfillStore) Close() error { return nil }

// TestBackfillEngineImpl_StreamOnly verifies that strategy "stream_only" never
// calls appendFn and HasPendingBackfills returns false after Run.
func TestBackfillEngineImpl_StreamOnly(t *testing.T) {
	store := newMockBackfillStore()
	idGen := event.NewIDGenerator()

	var appendCalls int
	appendFn := func(_ context.Context, _ *event.ChangeEvent) error {
		appendCalls++
		return nil
	}

	openConnFn := func(_ context.Context) (*pgx.Conn, error) {
		return nil, fmt.Errorf("should not be called for stream_only")
	}

	configs := []backfill.BackfillConfig{
		{
			SourceID:      "default",
			Schema:        "public",
			Table:         "orders",
			Strategy:      "stream_only",
			PKCols:        []string{"id"},
			NumPartitions: numEventLogPartitions,
		},
	}

	eng := backfill.NewBackfillEngine(configs, store, idGen, appendFn, openConnFn)

	err := eng.Run(context.Background())
	require.NoError(t, err)

	// stream_only: no snapshot rows appended (only the state transition is saved).
	assert.Equal(t, 0, appendCalls, "appendFn must not be called for stream_only")

	assert.False(t, eng.HasPendingBackfills(), "HasPendingBackfills must return false after stream_only Run")
}

// TestBackfillEngineImpl_SnapshotAndStream_OpenConnError verifies that a
// "snapshot_and_stream" strategy returns an error when openConnFn fails.
func TestBackfillEngineImpl_SnapshotAndStream_OpenConnError(t *testing.T) {
	store := newMockBackfillStore()
	idGen := event.NewIDGenerator()

	appendFn := func(_ context.Context, _ *event.ChangeEvent) error {
		return nil
	}

	openConnFn := func(_ context.Context) (*pgx.Conn, error) {
		return nil, fmt.Errorf("no db")
	}

	configs := []backfill.BackfillConfig{
		{
			SourceID:      "default",
			Schema:        "public",
			Table:         "orders",
			Strategy:      "snapshot_and_stream",
			PKCols:        []string{"id"},
			NumPartitions: numEventLogPartitions,
		},
	}

	eng := backfill.NewBackfillEngine(configs, store, idGen, appendFn, openConnFn)

	err := eng.Run(context.Background())
	require.Error(t, err, "Run must return error when openConnFn fails")
	assert.Contains(t, err.Error(), "no db")
}
