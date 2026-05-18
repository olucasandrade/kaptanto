package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/cluster"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
)

func runMongoPipeline(
	ctx context.Context,
	cfg *config.Config,
	ckStore checkpoint.CheckpointStore,
	el eventlog.EventLog,
	rtr *router.Router,
	cursorStore router.ConsumerCursorStore,
	cursorRun func(ctx context.Context),
	heartbeater *cluster.NodeHeartbeater,
	pm *cluster.PartitionManager,
	outputServer func(ctx context.Context) error,
	metrics *observability.KaptantoMetrics,
) error {
	// SRCC-03: cluster mode requires shared Postgres checkpoint store so a
	// replacement node can resume from the correct resume token position.
	if cfg.Cluster {
		pgStore, err := checkpoint.OpenPostgres(ctx, cfg.ClusterDSN)
		if err != nil {
			return fmt.Errorf("cluster: open mongo checkpoint store: %w", err)
		}
		defer pgStore.Close()
		ckStore = pgStore
	}

	if pm != nil {
		defer func() {
			if releaseErr := pm.ReleaseAll(context.Background()); releaseErr != nil {
				slog.Warn("cluster: release partitions on shutdown failed", "err", releaseErr)
			}
		}()
	}

	idGen := event.NewIDGenerator()
	tables := make([]string, 0, len(cfg.Tables))
	for t := range cfg.Tables {
		tables = append(tables, t)
	}

	dbName := extractDBFromMongoURI(cfg.Source)
	mongoCfg := mongodb.Config{
		URI:         cfg.Source,
		Database:    dbName,
		Collections: tables,
		SourceID:    "default",
	}

	connector, err := mongodb.NewWithEventLog(mongoCfg, ckStore, idGen, el)
	if err != nil {
		return fmt.Errorf("mongodb: create connector: %w", err)
	}

	appendFn := func(ctx context.Context, ev *event.ChangeEvent) error {
		return connector.AppendAndQueue(ctx, ev, nil)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { cursorRun(gctx); return nil })
	g.Go(func() error { return connector.Run(gctx) })
	g.Go(func() error { return rtr.Run(gctx) })
	g.Go(func() error { return outputServer(gctx) })
	if cfg.Cluster {
		g.Go(func() error { heartbeater.Run(gctx); return nil })
		g.Go(func() error { return pm.Run(gctx) })
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}

	if !connector.NeedsSnapshot() {
		slog.Info("kaptanto (mongodb) shut down cleanly")
		return nil
	}

	slog.Info("mongodb: InvalidResumeToken detected — running re-snapshot")
	wc := backfill.NewWatermarkChecker(el, numEventLogPartitions)
	snapCfg := mongodb.SnapshotConfig{
		Database:    dbName,
		Collections: tables,
		SourceID:    "default",
	}
	snap := mongodb.NewMongoSnapshot(snapCfg, nil, wc, idGen, appendFn)
	if snapErr := snap.Run(ctx); snapErr != nil && snapErr != context.Canceled {
		return fmt.Errorf("mongodb: snapshot failed: %w", snapErr)
	}

	connector2, err := mongodb.NewWithEventLog(mongoCfg, ckStore, idGen, el)
	if err != nil {
		return fmt.Errorf("mongodb: create connector after snapshot: %w", err)
	}
	g2, gctx2 := errgroup.WithContext(ctx)
	g2.Go(func() error { cursorRun(gctx2); return nil })
	g2.Go(func() error { return connector2.Run(gctx2) })
	g2.Go(func() error { return rtr.Run(gctx2) })
	g2.Go(func() error { return outputServer(gctx2) })
	if cfg.Cluster {
		g2.Go(func() error { heartbeater.Run(gctx2); return nil })
		g2.Go(func() error { return pm.Run(gctx2) })
	}
	if err := g2.Wait(); err != nil && err != context.Canceled {
		return err
	}

	slog.Info("kaptanto (mongodb) shut down cleanly")
	return nil
}

func extractDBFromMongoURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if len(u.Path) > 1 {
		return strings.TrimPrefix(u.Path, "/")
	}
	return ""
}
