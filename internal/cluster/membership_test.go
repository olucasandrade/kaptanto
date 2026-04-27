package cluster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNodeHeartbeater tests the NodeHeartbeater implementation.
// Integration tests require a real Postgres instance and skip when TEST_CLUSTER_DSN is unset.
func TestNodeHeartbeater(t *testing.T) {
	dsn := os.Getenv("TEST_CLUSTER_DSN")
	if dsn == "" {
		t.Skip("skipping: TEST_CLUSTER_DSN not set")
	}

	ctx := context.Background()

	t.Run("OpenNodeHeartbeater creates table and returns heartbeater", func(t *testing.T) {
		hb, err := OpenNodeHeartbeater(ctx, dsn, "test-node-open", "localhost:7654", 5*time.Second, 30)
		if err != nil {
			t.Fatalf("OpenNodeHeartbeater: %v", err)
		}
		defer hb.Close(ctx)

		if hb.nodeID != "test-node-open" {
			t.Errorf("nodeID: got %q, want %q", hb.nodeID, "test-node-open")
		}
		if hb.address != "localhost:7654" {
			t.Errorf("address: got %q, want %q", hb.address, "localhost:7654")
		}
	})

	t.Run("Run upserts node immediately then ticker", func(t *testing.T) {
		hb, err := OpenNodeHeartbeater(ctx, dsn, "test-node-run", "localhost:7655", 100*time.Millisecond, 30)
		if err != nil {
			t.Fatalf("OpenNodeHeartbeater: %v", err)
		}
		defer hb.Close(ctx)

		runCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()

		// Run in background; it upserts on start
		done := make(chan struct{})
		go func() {
			hb.Run(runCtx)
			close(done)
		}()

		// Wait for run to upsert at least once
		time.Sleep(50 * time.Millisecond)

		// Node should be visible
		nodes, err := hb.StaleNodes(ctx, 0)
		if err != nil {
			t.Fatalf("StaleNodes: %v", err)
		}

		found := false
		for _, n := range nodes {
			if n == "test-node-run" {
				found = true
			}
		}
		if !found {
			t.Error("expected test-node-run to appear in kaptanto_nodes after Run upsert")
		}

		<-done
	})

	t.Run("markOffline removes node row on graceful shutdown", func(t *testing.T) {
		hb, err := OpenNodeHeartbeater(ctx, dsn, "test-node-offline", "localhost:7656", 5*time.Second, 30)
		if err != nil {
			t.Fatalf("OpenNodeHeartbeater: %v", err)
		}
		defer hb.Close(ctx)

		// Upsert node manually
		if err := hb.upsert(ctx); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		// Mark offline
		if err := hb.markOffline(ctx); err != nil {
			t.Fatalf("markOffline: %v", err)
		}

		// Node should no longer exist
		nodes, err := hb.StaleNodes(ctx, 0)
		if err != nil {
			t.Fatalf("StaleNodes: %v", err)
		}
		for _, n := range nodes {
			if n == "test-node-offline" {
				t.Error("expected test-node-offline to be removed after markOffline")
			}
		}
	})

	t.Run("StaleNodes with threshold 0 returns all nodes", func(t *testing.T) {
		hb, err := OpenNodeHeartbeater(ctx, dsn, "test-node-stale", "localhost:7657", 5*time.Second, 30)
		if err != nil {
			t.Fatalf("OpenNodeHeartbeater: %v", err)
		}
		defer hb.Close(ctx)

		if err := hb.upsert(ctx); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		nodes, err := hb.StaleNodes(ctx, 0)
		if err != nil {
			t.Fatalf("StaleNodes(0): %v", err)
		}

		found := false
		for _, n := range nodes {
			if n == "test-node-stale" {
				found = true
			}
		}
		if !found {
			t.Error("StaleNodes(threshold=0) should return all nodes including test-node-stale")
		}
	})

	t.Run("StaleNodes returns empty slice not nil when no stale nodes", func(t *testing.T) {
		hb, err := OpenNodeHeartbeater(ctx, dsn, "test-node-fresh", "localhost:7658", 5*time.Second, 30)
		if err != nil {
			t.Fatalf("OpenNodeHeartbeater: %v", err)
		}
		defer hb.Close(ctx)

		if err := hb.upsert(ctx); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		// threshold=86400s (1 day) — recently inserted node should not be stale
		nodes, err := hb.StaleNodes(ctx, 86400)
		if err != nil {
			t.Fatalf("StaleNodes(86400): %v", err)
		}
		if nodes == nil {
			t.Error("StaleNodes must return empty slice (not nil) when no stale nodes")
		}
	})
}

// TestNodeHeartbeatSQLConstants validates SQL constants without Postgres connection.
func TestNodeHeartbeatSQLConstants(t *testing.T) {
	// Table schema must use TIMESTAMPTZ and JSONB
	if !strings.Contains(createNodesTableSQL, "TIMESTAMPTZ") {
		t.Error("createNodesTableSQL must use TIMESTAMPTZ for last_seen")
	}
	if !strings.Contains(createNodesTableSQL, "JSONB") {
		t.Error("createNodesTableSQL must use JSONB for partition_assignments")
	}
	if !strings.Contains(createNodesTableSQL, "kaptanto_nodes") {
		t.Error("createNodesTableSQL must create kaptanto_nodes table")
	}

	// Upsert SQL must use $N placeholders
	if !strings.Contains(upsertNodeSQL, "$1") {
		t.Error("upsertNodeSQL must use $N placeholders")
	}
	if !strings.Contains(upsertNodeSQL, "ON CONFLICT") {
		t.Error("upsertNodeSQL must use ON CONFLICT upsert")
	}
	if !strings.Contains(upsertNodeSQL, "NOW()") {
		t.Error("upsertNodeSQL must use NOW() for last_seen")
	}

	// Delete SQL for markOffline
	if !strings.Contains(deleteNodeSQL, "$1") {
		t.Error("deleteNodeSQL must use $1 placeholder")
	}
	if !strings.Contains(deleteNodeSQL, "DELETE") {
		t.Error("deleteNodeSQL must be a DELETE statement")
	}

	// StaleNodes SQL
	if !strings.Contains(staleNodesSQL, "$1") {
		t.Error("staleNodesSQL must use $1 placeholder")
	}
	if !strings.Contains(staleNodesSQL, "last_seen") {
		t.Error("staleNodesSQL must filter by last_seen")
	}
}

// TestNodeIDFallback validates nodeID derivation when empty string provided.
func TestNodeIDFallback(t *testing.T) {
	id := deriveNodeID("")
	if id == "" {
		t.Error("deriveNodeID should never return empty string")
	}
}
