// Package cmd implements the kaptanto CLI using cobra.
package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"net/url"

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
	"github.com/olucasandrade/kaptanto/internal/output"
	grpcoutput "github.com/olucasandrade/kaptanto/internal/output/grpc"
	"github.com/olucasandrade/kaptanto/internal/output/sse"
	"github.com/olucasandrade/kaptanto/internal/output/stdout"
	"github.com/olucasandrade/kaptanto/internal/router"
	mongodb "github.com/olucasandrade/kaptanto/internal/source/mongodb"
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
		// SilenceUsage prevents cobra from printing usage on non-flag errors.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, _ := cmd.Flags().GetString("config")
			sourceDSN, _ := cmd.Flags().GetString("source")

			// Guard: at least one of --source or --config is required.
			if configPath == "" && sourceDSN == "" {
				return fmt.Errorf("--source or --config is required")
			}

			// Load config file if provided, otherwise start with defaults.
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

			// Merge CLI flags on top (flags always win — 12-factor behavior).
			if err := config.Merge(cfg, cmd); err != nil {
				return err
			}

			// Post-merge validation: source must be set.
			if cfg.Source == "" {
				return fmt.Errorf("source is required: set via --source flag or 'source:' in config file")
			}

			// Graceful shutdown: cancel context on SIGTERM/SIGINT.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return runPipeline(ctx, cfg)
		},
	}

	// CFG-01: Source and table selection flags.
	root.PersistentFlags().String("source", "", "database connection string (e.g. postgres://user:pass@host/db)")
	root.PersistentFlags().StringArray("tables", nil, "tables to replicate, e.g. --tables public.orders --tables public.users")
	root.PersistentFlags().String("config", "", "path to YAML config file (flags take precedence over file)")

	// CFG-01: Output and server flags.
	root.PersistentFlags().String("output", "stdout", "output mode: stdout | sse | grpc")
	root.PersistentFlags().Int("port", 7654, "TCP port for SSE / gRPC server")

	// CFG-01: Storage flags.
	root.PersistentFlags().String("data-dir", "./data", "directory for the embedded Event Log and checkpoint store")
	root.PersistentFlags().Duration("retention", 0, "Event Log retention period (e.g. 24h, 7d); 0 applies the built-in default of 1h at runtime")

	// CFG-01: HA flags.
	root.PersistentFlags().Bool("ha", false, "enable high-availability mode (uses Postgres advisory locks; requires --source to point to a shared Postgres instance)")
	root.PersistentFlags().String("node-id", "", "unique node identifier for HA mode")

	// CFG-01: Source identity (used for replication slot and publication naming).
	root.PersistentFlags().String("source-id", "default", "logical source identifier; determines slot name (kaptanto_<id>) and publication name (kaptanto_pub_<id>)")

	// CFG-01: Cluster flags (Phase 14: shared Postgres state for cursor positions,
	// backfill progress, and node membership across replicas).
	root.PersistentFlags().Bool("cluster", false, "enable cluster mode with shared Postgres state")
	root.PersistentFlags().String("cluster-dsn", "", "Postgres DSN for cluster coordination tables (required when --cluster is set)")

	// EVLOG-03: NATS JetStream cluster flags (Phase 15: distributed event log).
	root.PersistentFlags().StringSlice("cluster-peers", nil, "NATS JetStream cluster peer addresses (e.g. node2:6222,node3:6222); required when --cluster is set for 3-node Raft")
	root.PersistentFlags().Int("nats-cluster-port", 6222, "NATS JetStream cluster route port for this node (default 6222)")

	// OBS-03: Observability flags.
	root.PersistentFlags().String("log-level", "info", "log verbosity: debug | info | warn | error")

	root.Version = version.Version
	root.AddCommand(newVersionCmd())

	// PersistentPreRunE initializes structured JSON logging before any subcommand runs.
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

// rootCmd is the singleton used by Execute. Tests use NewRootCmd() to get
// an isolated instance with separate flag sets.
var rootCmd = NewRootCmd()

// Execute runs the root command with os.Args. It writes output to os.Stdout
// and returns the first error encountered.
func Execute() error {
	return rootCmd.Execute()
}

// ExecuteWithArgs runs a fresh root command with the given args, writing
// output and error text to out. This is the test-friendly entry point.
func ExecuteWithArgs(args []string, out io.Writer) error {
	root := NewRootCmd()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	return root.Execute()
}

// buildTableFilters pre-parses all per-table row and column filters from config.
// Returns an error immediately if any WHERE expression is syntactically invalid (CFG-06).
// Returns nil maps (not empty maps) when cfg.Tables is nil/empty — nil map lookups are safe in Go.
func buildTableFilters(tables map[string]config.TableConfig) (
	map[string]*output.RowFilter,
	map[string][]string,
	error,
) {
	if len(tables) == 0 {
		return nil, nil, nil
	}
	rowFilters := make(map[string]*output.RowFilter, len(tables))
	colFilters := make(map[string][]string, len(tables))
	for table, tc := range tables {
		rf, err := output.ParseRowFilter(tc.Where)
		if err != nil {
			return nil, nil, fmt.Errorf("table %q where filter: %w", table, err)
		}
		rowFilters[table] = rf
		if len(tc.Columns) > 0 {
			colFilters[table] = tc.Columns
		}
	}
	return rowFilters, colFilters, nil
}

// buildBackfillConfigs converts the config.Tables map into BackfillConfig entries
// for BackfillEngineImpl construction.
//
// Defaults applied when config.TableConfig has no backfill-specific fields:
//   - Strategy: "snapshot_and_stream"
//   - PKCols: ["id"] — assumes a single "id" primary key column
//   - NumPartitions: numEventLogPartitions (must match eventlog.Open call)
//
// PKCols default is documented: tables using composite or non-"id" primary keys
// must configure pk_cols in a future config extension (Phase 7.4 scope limit).
func buildBackfillConfigs(tables map[string]config.TableConfig, sourceID string) []backfill.BackfillConfig {
	configs := make([]backfill.BackfillConfig, 0, len(tables))
	for tableKey := range tables {
		schema, table := "", tableKey
		if parts := strings.SplitN(tableKey, ".", 2); len(parts) == 2 {
			schema, table = parts[0], parts[1]
		}
		configs = append(configs, backfill.BackfillConfig{
			SourceID:      sourceID,
			Schema:        schema,
			Table:         table,
			Strategy:      "snapshot_and_stream",
			PKCols:        []string{"id"},
			NumPartitions: numEventLogPartitions,
		})
	}
	return configs
}

// runPipeline starts the full kaptanto pipeline with the merged configuration.
// It blocks until ctx is cancelled, then gracefully drains in-flight events and
// flushes all checkpoint and cursor stores before returning.
func runPipeline(ctx context.Context, cfg *config.Config) error {
	slog.Info("kaptanto starting",
		"source", cfg.Source,
		"output", cfg.Output,
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
	)

	// Guard: HA mode requires a Postgres source DSN. MongoDB sources do not
	// support advisory lock leader election via pgx. Reject early with a clear
	// error rather than letting ha.NewLeaderElector fail with a pgx driver error.
	if cfg.HA && cfg.SourceType() == "mongodb" {
		return fmt.Errorf("ha: --ha requires a Postgres source DSN; MongoDB source detected (%s)", cfg.Source)
	}

	// Guard: --cluster requires --cluster-dsn. Fail before any connection is
	// attempted so the user gets a clear error rather than a pgx connection failure.
	if cfg.Cluster && cfg.ClusterDSN == "" {
		return fmt.Errorf("--cluster-dsn is required when --cluster is set")
	}

	// HA election — must complete before any pipeline components start so that
	// only the leader opens the replication slot and writes checkpoints.
	var pgStore *checkpoint.PostgresStore
	if cfg.HA {
		// 1. Create LeaderElector with a dedicated Postgres connection.
		elector, err := ha.NewLeaderElector(ctx, cfg.Source)
		if err != nil {
			return fmt.Errorf("ha: connect for leader election: %w", err)
		}
		defer elector.Close()

		// 2. Compete for advisory lock — blocks in standby until acquired or ctx cancelled.
		slog.Info("ha: entering standby, polling for advisory lock")
		if err := elector.RunStandby(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("ha: standby interrupted: %w", err)
		}
		slog.Info("ha: advisory lock acquired — this instance is now the leader")

		// 3. Open shared Postgres checkpoint store. The new leader loads LSN from
		// this shared table so takeover resumes from where the old leader left off (HA-03).
		pgStore, err = checkpoint.OpenPostgres(ctx, cfg.Source)
		if err != nil {
			return fmt.Errorf("ha: open postgres checkpoint store: %w", err)
		}
		defer pgStore.Close()
	}

	// 1. Create data directory (Badger does not create parent dirs).
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// 2. Parse retention — "" means use 1h default.
	retention := time.Hour
	if cfg.Retention != "" {
		d, err := time.ParseDuration(cfg.Retention)
		if err != nil {
			return fmt.Errorf("parse retention %q: %w", cfg.Retention, err)
		}
		retention = d
	}

	// 3. Open event log.
	// Cluster mode: NatsEventLog (Raft-replicated JetStream, SyncAlways=true, EVLOG-01/02).
	// Non-cluster mode: BadgerEventLog (local Badger, unchanged pre-Phase-15 behaviour).
	hostname, _ := os.Hostname()
	var el eventlog.EventLog
	var elPing func() error
	// SRCC-02: walElector is non-nil only in cluster+Postgres mode. Declared here so it
	// is in scope for SetEpochGetter (Insertion B) and errgroup.Go (Insertion C) below.
	var walElector *cluster.WalLeaderElector
	if cfg.Cluster {
		natsClusterPort := cfg.NatsClusterPort
		if natsClusterPort == 0 {
			natsClusterPort = 6222
		}
		nodeID := cfg.NodeID
		if nodeID == "" {
			// Hostname is a safe default; cluster mode should always set --node-id explicitly
			// for NATS ServerName uniqueness across nodes.
			nodeID = hostname
		}
		natsCfg := eventlog.NatsEventLogConfig{
			Server: eventlog.NatsServerConfig{
				NodeID:      nodeID,
				ClusterPort: natsClusterPort,
				Advertise:   fmt.Sprintf("%s:%d", hostname, natsClusterPort),
				Peers:       cfg.ClusterPeers,
				StoreDir:    filepath.Join(cfg.DataDir, "nats"),
				SyncAlways:  true, // CHK-01: fsync before ack (EVLOG-02)
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

		// SRCC-02: Create WAL leader elector using the existing NATS connection.
		// Wired into the connector only if source is Postgres (MongoDB pipeline is
		// dispatched before the connector is constructed below — walElector stays nil
		// for MongoDB callers, which is the correct non-fenced behaviour).
		walElector, err = cluster.NewWalLeaderElector(ctx, natsEl.Conn(), nodeID)
		if err != nil {
			return fmt.Errorf("cluster: open wal leader elector: %w", err)
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

	// 4. Open checkpoint store (source LSN persistence).
	// HA mode: use the shared PostgresStore opened above (already deferred Close).
	// Non-HA mode: use local SQLiteStore.
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

	// 5. Open cursor store (consumer resume cursors).
	// Cluster mode: use PostgresCursorStore backed by the shared Postgres instance.
	// Non-cluster mode: use SQLiteCursorStore backed by a local file — existing
	// behaviour is byte-for-byte identical to pre-Phase-14 (else branch unchanged).
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
		// Separate file from checkpoint.db to avoid coupling the two store implementations.
		// Pragmas are applied explicitly after open — URI pragma encoding is unreliable
		// with modernc.org/sqlite and triggers "out of memory" errors.
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

	// 6. Pre-parse per-table filters (CFG-05, CFG-06). Fail fast on invalid WHERE.
	rowFilters, colFilters, err := buildTableFilters(cfg.Tables)
	if err != nil {
		return err
	}

	// Phase 16: Open cluster partition components before Router so cursorStore
	// can be epoch-fenced before being passed to NewRouter (DLVR-02).
	var pm *cluster.PartitionManager
	var heartbeater *cluster.NodeHeartbeater
	if cfg.Cluster {
		nodeAddr := fmt.Sprintf("%s:%d", hostname, cfg.Port)
		nodeID := cfg.NodeID
		if nodeID == "" {
			nodeID = hostname
		}
		var hbErr error
		heartbeater, hbErr = cluster.OpenNodeHeartbeater(ctx, cfg.ClusterDSN, nodeID, nodeAddr, 5*time.Second, 30)
		if hbErr != nil {
			return fmt.Errorf("open node heartbeater: %w", hbErr)
		}
		defer heartbeater.Close(context.Background())

		partStore, psErr := cluster.OpenPartitionStore(ctx, cfg.ClusterDSN, heartbeater.NodeID())
		if psErr != nil {
			return fmt.Errorf("open partition store: %w", psErr)
		}
		defer partStore.Close(context.Background())

		// rtr=nil here; injected via SetRouter after NewRouter below.
		pm = cluster.NewPartitionManager(partStore, heartbeater, nil, 5*time.Second)

		// Wrap cursorStore with epoch fencing before passing to Router.
		cursorStore = cluster.NewEpochCursorStore(cursorStore, pm)
	}

	// 7. Create router.
	rtr := router.NewRouter(el, numEventLogPartitions, cursorStore)
	if cfg.Cluster {
		pm.SetRouter(rtr)
	}

	// 8. Create observability (metrics + health).
	// Health probes are built incrementally so the ha_lock probe can be
	// conditionally appended when HA mode is active.
	metrics := observability.NewKaptantoMetrics()
	healthProbes := []observability.HealthProbe{
		{
			Name:  "eventlog",
			Check: elPing,
		},
		{
			Name:  "checkpoint",
			Check: ckProbe,
		},
		{
			Name:  "cursors",
			Check: cursorPing,
		},
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

	// Thread metrics into components that write Prometheus gauges/counters.
	rtr.SetMetrics(metrics)
	cursorSetMetrics(metrics)

	// 9. Wire output — register consumer(s) BEFORE starting the router.
	var outputServer func(ctx context.Context) error
	switch cfg.Output {
	case "stdout":
		writer := stdout.NewStdoutWriter(os.Stdout)
		writer.SetMetrics(metrics)
		rtr.Register(writer)
		outputServer = func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		}
	case "sse":
		sseServer := sse.NewSSEServer(rtr, cursorStore, metrics, "*", 15*time.Second, rowFilters, colFilters)
		mux := http.NewServeMux()
		mux.Handle("/events", sseServer)
		mux.Handle("/metrics", metrics.Handler())
		mux.Handle("/healthz", healthHandler)
		srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Handler: mux}
		outputServer = func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("sse server: %w", err)
			}
			return nil
		}
	case "grpc":
		grpcSvc := grpcoutput.NewGRPCServer(rtr, cursorStore, metrics, rowFilters, colFilters)
		grpcSrv := grpcoutput.NewGRPCNetServer(grpcSvc)
		var lis net.Listener
		lis, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			return fmt.Errorf("grpc listen: %w", err)
		}
		// Observability HTTP on cfg.Port+1 (gRPC occupies cfg.Port with H2 framing).
		obsMux := http.NewServeMux()
		obsMux.Handle("/metrics", metrics.Handler())
		obsMux.Handle("/healthz", healthHandler)
		obsSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port+1), Handler: obsMux}
		outputServer = func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				grpcSrv.GracefulStop()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = obsSrv.Shutdown(shutdownCtx)
			}()
			go func() {
				if err := obsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Warn("obs server error", "err", err)
				}
			}()
			if err := grpcSrv.Serve(lis); err != nil {
				return fmt.Errorf("grpc server: %w", err)
			}
			return nil
		}
	default:
		return fmt.Errorf("unknown output mode %q: use stdout, sse, or grpc", cfg.Output)
	}

	// 10. Dispatch to source-specific pipeline based on DSN type.
	// Auto-detection from DSN prefix: "mongodb://" or "mongodb+srv://" → MongoDB;
	// everything else → Postgres (existing pipeline unchanged).
	if cfg.SourceType() == "mongodb" {
		return runMongoPipeline(ctx, cfg, ckStore, el, rtr, cursorStore, cursorRun, heartbeater, pm, outputServer, metrics)
	}

	// 10. Build Postgres connector and BackfillEngine.
	// Two-step construction avoids circular dependency:
	//   Step a: create connector (nil engine placeholder)
	//   Step b: create engine with connector.AppendAndQueue as appendFn
	//   Step c: inject engine into connector via SetBackfillEngine
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

	// 10a. Connector — nil engine placeholder resolved in step 10d.
	connector := postgres.NewWithBackfill(connCfg, ckStore, idGen, el, nil)
	connector.SetMetrics(metrics)

	// SRCC-01: Inject epoch fencing into the Postgres connector.
	// The connector drops sendStandbyStatus calls whenever EpochGetter returns isLeader=false.
	// walElector is nil in non-cluster mode — SetEpochGetter is a no-op for nil getter.
	if walElector != nil {
		connector.SetEpochGetter(walElector.EpochGetter)
	}

	// 10b. BackfillStore — cursor persistence on every batch (BKF-03).
	// Cluster mode: use PostgresBackfillStore backed by the shared Postgres instance
	// so backfill can resume on a different node from the last keyset cursor (STATE-03).
	// Non-cluster mode: use SQLiteBackfillStore backed by a local file — unchanged.
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

	// 10c. BackfillConfigs — one per configured table.
	bkConfigs := buildBackfillConfigs(cfg.Tables, connCfg.SourceID)

	// 10d. Build BackfillEngineImpl with connector.AppendAndQueue as appendFn.
	// openConnFn opens a dedicated pgx.Conn for snapshot SELECT queries
	// (must NOT reuse the replication connection — SRC-01 constraint).
	openConnFn := func(ctx context.Context) (*pgx.Conn, error) {
		return pgx.Connect(ctx, cfg.Source)
	}
	bkEng := backfill.NewBackfillEngine(bkConfigs, bkStore, idGen, connector.AppendAndQueue, openConnFn)

	// 10e. Wire WatermarkChecker — must use same numPartitions as EventLog (BKF-02).
	wc := backfill.NewWatermarkChecker(el, numEventLogPartitions)
	bkEng.SetWatermark(wc)

	// 10f. Inject engine into connector (breaks construction-time circular dependency).
	connector.SetBackfillEngine(bkEng)

	// 11. Run all components under errgroup — first error cancels the group context.
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
	// Phase 16: Release partition ownership after all goroutines drain.
	// PostgresCursorStore.Run flushes cursors on ctx cancel before g.Wait returns.
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

// runMongoPipeline starts the MongoDB CDC pipeline. It is called from
// runPipeline when cfg.SourceType() == "mongodb".
//
// It creates a MongoDBConnector with the shared EventLog, runs it in an
// errgroup alongside the router and output server, and handles
// NeedsSnapshot() by running MongoSnapshot with WatermarkChecker then
// restarting the connector (single re-snapshot attempt).
func runMongoPipeline(
	ctx context.Context,
	cfg *config.Config,
	ckStore checkpoint.CheckpointStore,
	el eventlog.EventLog,
	rtr *router.Router,
	cursorStore router.ConsumerCursorStore,
	cursorRun func(ctx context.Context),
	heartbeater *cluster.NodeHeartbeater,  // nil when !cfg.Cluster
	pm *cluster.PartitionManager,          // nil when !cfg.Cluster
	outputServer func(ctx context.Context) error,
	metrics *observability.KaptantoMetrics,
) error {
	// SRCC-03: In cluster mode, MongoDB resume tokens must be written to the shared
	// Postgres store (cfg.ClusterDSN) so a replacement node can resume from the correct
	// position. Override the passed ckStore with a PostgresStore backed by ClusterDSN.
	// Non-cluster mode: ckStore is the SQLite store passed from runPipeline (unchanged).
	if cfg.Cluster {
		pgStore, err := checkpoint.OpenPostgres(ctx, cfg.ClusterDSN)
		if err != nil {
			return fmt.Errorf("cluster: open mongo checkpoint store: %w", err)
		}
		defer pgStore.Close()
		ckStore = pgStore
	}

	idGen := event.NewIDGenerator()

	// Build collection list from cfg.Tables keys (or empty for all).
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

	// appendFn wraps connector.AppendAndQueue for use by MongoSnapshot.
	// The connector's AppendAndQueue also persists the resume token, but for
	// snapshot rows there is no resume token — we use a nil token sentinel.
	appendFn := func(ctx context.Context, ev *event.ChangeEvent) error {
		return connector.AppendAndQueue(ctx, ev, nil)
	}

	// Run first connector pass.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { cursorRun(gctx); return nil })
	g.Go(func() error { return connector.Run(gctx) })
	g.Go(func() error { return rtr.Run(gctx) })
	g.Go(func() error { return outputServer(gctx) })

	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}

	// If the connector detected an InvalidResumeToken, run a snapshot then
	// restart streaming. Single re-snapshot attempt (retry loop is future work).
	if connector.NeedsSnapshot() {
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

		// Restart the connector after snapshot.
		connector2, err := mongodb.NewWithEventLog(mongoCfg, ckStore, idGen, el)
		if err != nil {
			return fmt.Errorf("mongodb: create connector after snapshot: %w", err)
		}
		g2, gctx2 := errgroup.WithContext(ctx)
		g2.Go(func() error { cursorRun(gctx2); return nil })
		g2.Go(func() error { return connector2.Run(gctx2) })
		g2.Go(func() error { return rtr.Run(gctx2) })
		g2.Go(func() error { return outputServer(gctx2) })
		if err := g2.Wait(); err != nil && err != context.Canceled {
			return err
		}
	}

	slog.Info("kaptanto (mongodb) shut down cleanly")
	return nil
}

// extractDBFromMongoURI parses the database name from a MongoDB URI path
// component (e.g., "mongodb://host/mydb" → "mydb").
// Returns an empty string if the path has no database component.
func extractDBFromMongoURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	// Path is "/dbname" — trim the leading slash.
	if len(u.Path) > 1 {
		return strings.TrimPrefix(u.Path, "/")
	}
	return ""
}
