// Package cmd implements the kaptanto CLI using cobra.
package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jackc/pgx/v5"
	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/cluster"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/ha"
	"github.com/olucasandrade/kaptanto/internal/logging"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	postgres "github.com/olucasandrade/kaptanto/internal/source/postgres"
	"github.com/olucasandrade/kaptanto/internal/version"
	"github.com/spf13/cobra"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// numEventLogPartitions is the partition count used for both the EventLog and
// WatermarkChecker — they must match for correct watermark deduplication (BKF-02).
const numEventLogPartitions = 64

// NewRootCmd constructs and returns the root cobra command with all persistent flags.
// It is exported so tests can create isolated instances without global state.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "kaptanto",
		Short: "Universal database Change Data Capture (CDC)",
		Long: `kaptanto streams database changes (inserts, updates, deletes) from Postgres
and MongoDB to stdout, SSE, or gRPC. It requires zero infrastructure beyond
the database itself and is distributed as a single static binary.

The name means "who captures" in Esperanto.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, _ := cmd.Flags().GetString("config")
			sourceDSN, _ := cmd.Flags().GetString("source")

			if configPath == "" && sourceDSN == "" {
				return fmt.Errorf("--source or --config is required")
			}

			var cfg *config.Config
			if configPath != "" {
				var err error
				cfg, err = config.Load(configPath)
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
			} else {
				cfg = config.Defaults()
			}

			if err := config.Merge(cfg, cmd); err != nil {
				return err
			}

			if cfg.Source == "" {
				return fmt.Errorf("source is required: set via --source flag or 'source:' in config file")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runPipeline(ctx, cfg)
		},
	}

	root.PersistentFlags().String("source", "", "database connection string (e.g. postgres://user:pass@host/db)")
	root.PersistentFlags().StringArray("tables", nil, "tables to replicate, e.g. --tables public.orders --tables public.users")
	root.PersistentFlags().String("config", "", "path to YAML config file (flags take precedence over file)")
	root.PersistentFlags().String("output", "stdout", "output mode: stdout | sse | grpc | nats | sqs | kafka | pubsub | rabbitmq")
	root.PersistentFlags().Int("port", 7654, "TCP port for SSE / gRPC server")
	root.PersistentFlags().String("data-dir", "./data", "directory for the embedded Event Log and checkpoint store")
	root.PersistentFlags().Duration("retention", 0, "Event Log retention period (e.g. 24h, 7d); 0 applies the built-in default of 1h at runtime")
	root.PersistentFlags().Bool("ha", false, "enable high-availability mode (uses Postgres advisory locks; requires --source to point to a shared Postgres instance)")
	root.PersistentFlags().String("node-id", "", "unique node identifier for HA mode")
	root.PersistentFlags().String("source-id", "default", "logical source identifier; determines slot name (kaptanto_<id>) and publication name (kaptanto_pub_<id>)")
	root.PersistentFlags().Bool("cluster", false, "enable cluster mode with shared Postgres state")
	root.PersistentFlags().String("cluster-dsn", "", "Postgres DSN for cluster coordination tables (required when --cluster is set)")
	root.PersistentFlags().StringSlice("cluster-peers", nil, "NATS JetStream cluster peer addresses (e.g. node2:6222,node3:6222); required when --cluster is set for 3-node Raft")
	root.PersistentFlags().Int("nats-cluster-port", 6222, "NATS JetStream cluster route port for this node (default 6222)")
	root.PersistentFlags().String("log-level", "info", "log verbosity: debug | info | warn | error")

	root.Version = version.Version
	root.AddCommand(newVersionCmd())

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		level, err := cmd.Flags().GetString("log-level")
		if err != nil {
			level = "info"
		}
		logging.Setup(os.Stderr, level)
		return nil
	}

	return root
}

var rootCmd = NewRootCmd()

func Execute() error {
	return rootCmd.Execute()
}

func ExecuteWithArgs(args []string, out io.Writer) error {
	root := NewRootCmd()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	return root.Execute()
}

func runPipeline(ctx context.Context, cfg *config.Config) error {
	slog.Info("kaptanto starting",
		"source", cfg.Source,
		"output", cfg.Output,
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
	)

	if cfg.HA && cfg.SourceType() == "mongodb" {
		return fmt.Errorf("ha: --ha requires a Postgres source DSN; MongoDB source detected (%s)", cfg.Source)
	}
	if cfg.Cluster && cfg.ClusterDSN == "" {
		return fmt.Errorf("--cluster-dsn is required when --cluster is set")
	}

	// HA leader election — must complete before any pipeline component starts.
	var pgStore *checkpoint.PostgresStore
	if cfg.HA {
		elector, err := ha.NewLeaderElector(ctx, cfg.Source)
		if err != nil {
			return fmt.Errorf("ha: connect for leader election: %w", err)
		}
		defer elector.Close()

		slog.Info("ha: entering standby, polling for advisory lock")
		if err := elector.RunStandby(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("ha: standby interrupted: %w", err)
		}
		slog.Info("ha: advisory lock acquired — this instance is now the leader")

		pgStore, err = checkpoint.OpenPostgres(ctx, cfg.Source)
		if err != nil {
			return fmt.Errorf("ha: open postgres checkpoint store: %w", err)
		}
		defer pgStore.Close()
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	retention := time.Hour
	if cfg.Retention != "" {
		d, err := time.ParseDuration(cfg.Retention)
		if err != nil {
			return fmt.Errorf("parse retention %q: %w", cfg.Retention, err)
		}
		retention = d
	}

	hostname, _ := os.Hostname()
	var el eventlog.EventLog
	var elPing func() error
	var walElector *cluster.WalLeaderElector
	if cfg.Cluster {
		natsClusterPort := cfg.NatsClusterPort
		if natsClusterPort == 0 {
			natsClusterPort = 6222
		}
		nodeID := cfg.NodeID
		if nodeID == "" {
			nodeID = hostname
		}
		natsCfg := eventlog.NatsEventLogConfig{
			Server: eventlog.NatsServerConfig{
				NodeID:      nodeID,
				ClusterPort: natsClusterPort,
				Advertise:   fmt.Sprintf("%s:%d", hostname, natsClusterPort),
				Peers:       cfg.ClusterPeers,
				StoreDir:    filepath.Join(cfg.DataDir, "nats"),
				SyncAlways:  true,
			},
			NumPartitions: numEventLogPartitions,
			Retention:     retention,
		}
		natsEl, err := eventlog.OpenNats(natsCfg)
		if err != nil {
			return fmt.Errorf("open nats event log: %w", err)
		}
		defer natsEl.Close()
		el = natsEl
		elPing = natsEl.Ping

		var werr error
		walElector, werr = cluster.NewWalLeaderElector(ctx, natsEl.Conn(), nodeID)
		if werr != nil {
			return fmt.Errorf("cluster: open wal leader elector: %w", werr)
		}
	} else {
		badgerEl, err := eventlog.Open(filepath.Join(cfg.DataDir, "events"), numEventLogPartitions, retention)
		if err != nil {
			return fmt.Errorf("open event log: %w", err)
		}
		defer badgerEl.Close()
		el = badgerEl
		elPing = badgerEl.Ping
	}

	var ckStore checkpoint.CheckpointStore
	var ckProbe func() error
	if cfg.HA {
		ckStore = pgStore
		ckProbe = func() error { return pgStore.Ping(context.Background()) }
	} else {
		sqliteStore, err := checkpoint.Open(filepath.Join(cfg.DataDir, "checkpoint.db"))
		if err != nil {
			return fmt.Errorf("open checkpoint store: %w", err)
		}
		defer sqliteStore.Close()
		ckStore = sqliteStore
		ckProbe = sqliteStore.Ping
	}

	var cursorStore router.ConsumerCursorStore
	var cursorPing func() error
	var cursorSetMetrics func(*observability.KaptantoMetrics)
	var cursorRun func(ctx context.Context)
	if cfg.Cluster {
		pgCursorStore, err := checkpoint.OpenPostgresCursorStore(ctx, cfg.ClusterDSN, 5*time.Second)
		if err != nil {
			return fmt.Errorf("open postgres cursor store: %w", err)
		}
		defer pgCursorStore.Close()
		cursorStore = pgCursorStore
		cursorPing = func() error { return pgCursorStore.Ping(context.Background()) }
		cursorSetMetrics = pgCursorStore.SetMetrics
		cursorRun = pgCursorStore.Run
	} else {
		cursorDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "cursors.db"))
		if err != nil {
			return fmt.Errorf("open cursor db: %w", err)
		}
		defer cursorDB.Close()
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL;",
			"PRAGMA synchronous=NORMAL;",
		} {
			if _, err := cursorDB.Exec(pragma); err != nil {
				return fmt.Errorf("cursor db pragma %q: %w", pragma, err)
			}
		}
		sqliteCursorStore, err := checkpoint.NewSQLiteCursorStore(cursorDB, 5*time.Second)
		if err != nil {
			return fmt.Errorf("create cursor store: %w", err)
		}
		cursorStore = sqliteCursorStore
		cursorPing = sqliteCursorStore.Ping
		cursorSetMetrics = sqliteCursorStore.SetMetrics
		cursorRun = sqliteCursorStore.Run
	}

	rowFilters, colFilters, err := buildTableFilters(cfg.Tables)
	if err != nil {
		return err
	}

	var pm *cluster.PartitionManager
	var heartbeater *cluster.NodeHeartbeater
	if cfg.Cluster {
		nodeAddr := fmt.Sprintf("%s:%d", hostname, cfg.Port)
		nodeID := cfg.NodeID
		if nodeID == "" {
			nodeID = hostname
		}
		var hbErr error
		heartbeater, hbErr = cluster.OpenNodeHeartbeater(ctx, cfg.ClusterDSN, nodeID, nodeAddr, 5*time.Second)
		if hbErr != nil {
			return fmt.Errorf("open node heartbeater: %w", hbErr)
		}
		defer heartbeater.Close(context.Background())

		partStore, psErr := cluster.OpenPartitionStore(ctx, cfg.ClusterDSN, heartbeater.NodeID())
		if psErr != nil {
			return fmt.Errorf("open partition store: %w", psErr)
		}
		defer partStore.Close(context.Background())

		pm = cluster.NewPartitionManager(partStore, heartbeater, nil, 5*time.Second)
		cursorStore = cluster.NewEpochCursorStore(cursorStore, pm)
	}

	rtr := router.NewRouter(el, numEventLogPartitions, cursorStore)
	if cfg.Cluster {
		pm.SetRouter(rtr)
	}

	metrics := observability.NewKaptantoMetrics()
	healthProbes := []observability.HealthProbe{
		{Name: "eventlog", Check: elPing},
		{Name: "checkpoint", Check: ckProbe},
		{Name: "cursors", Check: cursorPing},
		{
			Name: "postgres",
			Check: func() error {
				pCtx, pCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer pCancel()
				conn, err := pgx.Connect(pCtx, cfg.Source)
				if err != nil {
					return err
				}
				_ = conn.Close(context.Background())
				return nil
			},
		},
	}
	if cfg.HA {
		healthProbes = append(healthProbes, observability.HealthProbe{
			Name:  "ha_lock",
			Check: func() error { return pgStore.Ping(context.Background()) },
		})
	}
	healthHandler := observability.NewHealthHandler(healthProbes)

	rtr.SetMetrics(metrics)
	cursorSetMetrics(metrics)

	outputServer, err := buildOutputServer(cfg, rtr, cursorStore, metrics, healthHandler, healthProbes, rowFilters, colFilters)
	if err != nil {
		return err
	}

	if cfg.SourceType() == "mongodb" {
		return runMongoPipeline(ctx, cfg, ckStore, el, rtr, cursorStore, cursorRun, heartbeater, pm, outputServer, metrics)
	}

	tables := make([]string, 0, len(cfg.Tables))
	for t := range cfg.Tables {
		tables = append(tables, t)
	}
	sourceID := cfg.SourceID
	if sourceID == "" {
		sourceID = "default"
	}
	connCfg := postgres.Config{
		DSN:      cfg.Source,
		Tables:   tables,
		SourceID: sourceID,
	}
	connCfg.ApplyDefaults()
	idGen := event.NewIDGenerator()

	connector := postgres.NewWithBackfill(connCfg, ckStore, idGen, el, nil)
	connector.SetMetrics(metrics)
	if walElector != nil {
		connector.SetEpochGetter(walElector.EpochGetter)
	}

	var bkStore backfill.BackfillStore
	if cfg.Cluster {
		pgBkStore, err := backfill.OpenPostgresBackfillStore(ctx, cfg.ClusterDSN)
		if err != nil {
			return fmt.Errorf("open postgres backfill store: %w", err)
		}
		defer pgBkStore.Close()
		bkStore = pgBkStore
	} else {
		sqliteBkStore, err := backfill.OpenSQLiteBackfillStore(filepath.Join(cfg.DataDir, "backfill.db"))
		if err != nil {
			return fmt.Errorf("open backfill store: %w", err)
		}
		defer sqliteBkStore.Close()
		bkStore = sqliteBkStore
	}

	bkConfigs := buildBackfillConfigs(cfg.Tables, connCfg.SourceID)
	openConnFn := func(ctx context.Context) (*pgx.Conn, error) {
		return pgx.Connect(ctx, cfg.Source)
	}
	bkEng := backfill.NewBackfillEngine(bkConfigs, bkStore, idGen, connector.AppendAndQueue, openConnFn)
	bkEng.SetWatermark(backfill.NewWatermarkChecker(el, numEventLogPartitions))
	connector.SetBackfillEngine(bkEng)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { cursorRun(gctx); return nil })
	g.Go(func() error { return connector.Run(gctx) })
	g.Go(func() error { return rtr.Run(gctx) })
	g.Go(func() error { return outputServer(gctx) })
	if cfg.Cluster {
		g.Go(func() error { heartbeater.Run(gctx); return nil })
		g.Go(func() error { return pm.Run(gctx) })
		if walElector != nil {
			g.Go(func() error { return walElector.Run(gctx) })
		}
	}

	waitErr := g.Wait()
	if pm != nil {
		if releaseErr := pm.ReleaseAll(context.Background()); releaseErr != nil {
			slog.Warn("cluster: release partitions on shutdown failed", "err", releaseErr)
		}
	}
	if waitErr != nil && waitErr != context.Canceled {
		return waitErr
	}
	slog.Info("kaptanto shut down cleanly")
	return nil
}
