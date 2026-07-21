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
	"github.com/olucasandrade/kaptanto/internal/action"
	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/checkpoint"
	"github.com/olucasandrade/kaptanto/internal/cluster"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/dlq"
	"github.com/olucasandrade/kaptanto/internal/enrich"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/ha"
	"github.com/olucasandrade/kaptanto/internal/logging"
	"github.com/olucasandrade/kaptanto/internal/mcp"
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

			config.ApplyEnv(cfg)

			if cfg.Source == "" {
				return fmt.Errorf("source is required: set via --source flag or 'source:' in config file")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runPipeline(ctx, cfg)
		},
	}

	root.PersistentFlags().String("source", "", "database connection string (e.g. postgres://user:pass@host/db)")
	root.PersistentFlags().StringSlice("tables", nil, "tables to replicate, e.g. --tables public.orders,public.users or --tables public.orders --tables public.users")
	root.PersistentFlags().String("config", "", "path to YAML config file (flags take precedence over file)")
	root.PersistentFlags().String("output", "stdout", "output mode: stdout | sse | grpc | nats | sqs | kafka | pubsub | rabbitmq")
	root.PersistentFlags().Int("port", 7654, "TCP port for SSE / gRPC server")
	root.PersistentFlags().String("cors-origin", "", "SSE Access-Control-Allow-Origin value; empty (default) sends no CORS header, blocking cross-origin browser access to the stream")
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
	root.PersistentFlags().Bool("all-tables", false, "capture all tables in the database (requires explicit opt-in; default requires --tables or 'tables:' in config)")
	root.PersistentFlags().String("tls-cert", "", "path to TLS certificate PEM for SSE/gRPC server")
	root.PersistentFlags().String("tls-key", "", "path to TLS private key PEM for SSE/gRPC server")
	root.PersistentFlags().String("tls-client-ca", "", "path to CA PEM for client certificate verification (mTLS); requires --tls-cert and --tls-key")
	root.PersistentFlags().String("auth-token", "", "static bearer token required by SSE/gRPC clients (prefer KAPTANTO_AUTH_TOKEN env var to avoid leaking the secret in process listings)")
	root.PersistentFlags().Bool("insecure", false, "allow plaintext SSE/gRPC without TLS or auth token (not recommended for production; logs a security warning)")
	root.PersistentFlags().Bool("dlq-enabled", true, "enable the dead-letter queue for poison events (default on; --dlq-enabled=false restores pre-DLQ log-and-drop)")
	root.PersistentFlags().String("dlq-path", "", "path to the DLQ SQLite store (default <data-dir>/dlq.db)")
	root.PersistentFlags().Duration("dlq-retention", 0, "DLQ retention period (e.g. 168h); 0 keeps entries forever")

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

// ensureDataDir creates the runtime state directory with owner-only permissions
// (0o700). The directory holds the Badger event log and the SQLite
// checkpoint/cursor/backfill stores, all of which contain captured row data
// (potential PII at rest), so it must not be traversable by other local users.
func ensureDataDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

//nolint:gocyclo // pipeline assembly wires many optional components; splitting it would obscure the linear startup sequence. Tracked for incremental refactor.
func runPipeline(ctx context.Context, cfg *config.Config) error {
	slog.Info("kaptanto starting",
		"source", redactDSN(cfg.Source),
		"output", cfg.Output,
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
	)

	if cfg.HA && cfg.SourceType() == "mongodb" {
		return fmt.Errorf("ha: --ha requires a Postgres source DSN; MongoDB source detected (%s)", redactDSN(cfg.Source))
	}
	if cfg.Cluster && cfg.ClusterDSN == "" {
		return fmt.Errorf("--cluster-dsn is required when --cluster is set")
	}

	if cfg.SourceType() == "postgres" && len(cfg.Tables) == 0 && !cfg.AllowAllTables {
		return fmt.Errorf("no tables configured: use 'tables:' in config or --tables to specify tables to replicate, " +
			"or pass --all-tables to explicitly capture all tables in the database (caution: exposes all data)")
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
		defer func() { _ = pgStore.Close() }()
	}

	if err := ensureDataDir(cfg.DataDir); err != nil {
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
		defer func() { _ = natsEl.Close() }()
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
		defer func() { _ = badgerEl.Close() }()
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
		defer func() { _ = sqliteStore.Close() }()
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
		defer func() { _ = pgCursorStore.Close() }()
		cursorStore = pgCursorStore
		cursorPing = func() error { return pgCursorStore.Ping(context.Background()) }
		cursorSetMetrics = pgCursorStore.SetMetrics
		cursorRun = pgCursorStore.Run
	} else {
		cursorDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "cursors.db"))
		if err != nil {
			return fmt.Errorf("open cursor db: %w", err)
		}
		defer func() { _ = cursorDB.Close() }()
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
		defer func() { _ = heartbeater.Close(context.Background()) }()

		partStore, psErr := cluster.OpenPartitionStore(ctx, cfg.ClusterDSN, heartbeater.NodeID())
		if psErr != nil {
			return fmt.Errorf("open partition store: %w", psErr)
		}
		defer func() { _ = partStore.Close(context.Background()) }()

		pm = cluster.NewPartitionManager(partStore, heartbeater, nil, 5*time.Second)
		cursorStore = cluster.NewEpochCursorStore(cursorStore, pm)
	}

	metrics := observability.NewKaptantoMetrics()

	// Optional fail-open HTTP enricher (AIC-01/02). Wrap EventLog so every
	// Append/AppendBatch path (WAL + backfill) enriches before the durable
	// write — connectors and EventLog backends stay untouched.
	enricher, err := enrich.Compile(cfg.Enrichment, metrics)
	if err != nil {
		return err
	}
	el = enrich.Wrap(el, enricher)

	rtr := router.NewRouter(el, numEventLogPartitions, cursorStore)
	if cfg.Cluster {
		pm.SetRouter(rtr)
	}
	// Open the DLQ store. Enabled by default (nil/true); an explicit false
	// restores pre-DLQ log-and-drop behavior.
	var dlqStore dlq.Store
	if cfg.DLQ.Enabled == nil || *cfg.DLQ.Enabled {
		dlqPath := cfg.DLQ.Path
		if dlqPath == "" {
			dlqPath = filepath.Join(cfg.DataDir, "dlq.db")
		}
		store, err := dlq.Open(dlqPath)
		if err != nil {
			return fmt.Errorf("open dlq store: %w", err)
		}
		dlqStore = store
		defer func() { _ = store.Close() }()

		if cfg.DLQ.Retention != "" {
			ret, err := time.ParseDuration(cfg.DLQ.Retention)
			if err != nil {
				return fmt.Errorf("parse dlq retention %q: %w", cfg.DLQ.Retention, err)
			}
			if ret > 0 {
				retentionCtx, stopRetention := context.WithCancel(ctx)
				defer stopRetention()
				go func(ret time.Duration) {
					ticker := time.NewTicker(ret / 2)
					defer ticker.Stop()
					for {
						select {
						case <-retentionCtx.Done():
							return
						case <-ticker.C:
							cutoff := time.Now().UTC().Add(-ret)
							n, err := store.Purge(retentionCtx, dlq.Filter{OlderThan: cutoff})
							if err != nil {
								slog.Warn("dlq: retention purge failed", "err", err)
							} else if n > 0 {
								slog.Info("dlq: purged old entries", "count", n)
							}
						}
					}
				}(ret)
			}
		}
	}
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
	rtr.RetryScheduler().SetMetrics(metrics)
	cursorSetMetrics(metrics)
	rtr.SetDLQ(dlqStore)

	outputServer, err := buildOutputServer(cfg, rtr, cursorStore, metrics, healthHandler, healthProbes, rowFilters, colFilters)
	if err != nil {
		return err
	}

	// Build and register action consumers (after primary output, before source start).
	actionConsumers, err := action.BuildConsumers(cfg, metrics)
	if err != nil {
		return err
	}
	for _, ac := range actionConsumers {
		rtr.Register(ac)
	}
	if cfg.Output == "none" && len(actionConsumers) == 0 {
		return fmt.Errorf("output: none requires at least one configured action")
	}

	mcpServer, err := openMCPServer(cfg, metrics)
	if err != nil {
		return err
	}
	if mcpServer != nil {
		defer func() { _ = mcpServer.Close() }()
	}

	if cfg.SourceType() == "mongodb" {
		return runMongoPipeline(ctx, cfg, ckStore, el, rtr, cursorStore, cursorRun, heartbeater, pm, outputServer, metrics, mcpServer)
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
		DSN:            cfg.Source,
		Tables:         tables,
		SourceID:       sourceID,
		AllowAllTables: cfg.AllowAllTables,
	}
	connCfg.ApplyDefaults()
	idGen := event.NewIDGenerator()

	connector := postgres.NewWithBackfill(connCfg, ckStore, idGen, el, nil)
	connector.SetMetrics(metrics)
	if walElector != nil {
		connector.SetEpochGetter(walElector.EpochGetter)
	}
	if mcpServer != nil {
		mcpServer.SetSchemaProvider(&mcp.ParserSchemaProvider{Parser: connector.Parser()})
	}

	var bkStore backfill.BackfillStore
	if cfg.Cluster {
		pgBkStore, err := backfill.OpenPostgresBackfillStore(ctx, cfg.ClusterDSN)
		if err != nil {
			return fmt.Errorf("open postgres backfill store: %w", err)
		}
		defer func() { _ = pgBkStore.Close() }()
		bkStore = pgBkStore
	} else {
		sqliteBkStore, err := backfill.OpenSQLiteBackfillStore(filepath.Join(cfg.DataDir, "backfill.db"))
		if err != nil {
			return fmt.Errorf("open backfill store: %w", err)
		}
		defer func() { _ = sqliteBkStore.Close() }()
		bkStore = sqliteBkStore
	}

	// Discover each table's real primary-key columns before building backfill
	// configs — hardcoding "id" breaks any table whose PK isn't named "id",
	// silently mispaginates non-unique "id" columns, and can never
	// byte-match a composite-PK WAL key for BKF-02 watermark suppression.
	//
	// SRC-01: this uses its own short-lived pgx.Conn (connect, discover,
	// close), never the replication connection and never openConnFn's
	// lazily-opened snapshot connection below.
	var bkPKCols map[string][]string
	if len(cfg.Tables) > 0 {
		pkCtx, pkCancel := context.WithTimeout(ctx, 10*time.Second)
		pkConn, err := pgx.Connect(pkCtx, cfg.Source)
		if err != nil {
			pkCancel()
			return fmt.Errorf("backfill: connect for primary-key discovery: %w", err)
		}
		bkPKCols, err = discoverPrimaryKeys(pkCtx, pkConn, cfg.Tables)
		closeErr := pkConn.Close(context.Background())
		pkCancel()
		if err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
		if closeErr != nil {
			slog.Warn("backfill: close primary-key discovery connection", "err", closeErr)
		}
	}

	bkConfigs := buildBackfillConfigs(cfg.Tables, connCfg.SourceID, bkPKCols)
	openConnFn := func(ctx context.Context) (backfill.SnapshotConn, error) {
		return pgx.Connect(ctx, cfg.Source)
	}
	bkEng := backfill.NewBackfillEngineWithBatch(bkConfigs, bkStore, idGen, connector.AppendAndQueue, connector.AppendAndQueueBatch, openConnFn)
	bkEng.SetWatermark(backfill.NewWatermarkChecker(el, numEventLogPartitions))
	connector.SetBackfillEngine(bkEng)

	// MCP already opened above (shared with mongo path).
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { cursorRun(gctx); return nil })
	g.Go(func() error { return connector.Run(gctx) })
	g.Go(func() error { return rtr.Run(gctx) })
	g.Go(func() error { return outputServer(gctx) })
	if mcpServer != nil {
		g.Go(func() error { return mcpServer.Run(gctx) })
	}
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
