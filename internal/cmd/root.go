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

	"golang.org/x/sync/errgroup"

	"github.com/kaptanto/kaptanto/internal/backfill"
	"github.com/kaptanto/kaptanto/internal/checkpoint"
	"github.com/kaptanto/kaptanto/internal/config"
	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/logging"
	"github.com/kaptanto/kaptanto/internal/observability"
	"github.com/kaptanto/kaptanto/internal/output"
	grpcoutput "github.com/kaptanto/kaptanto/internal/output/grpc"
	"github.com/kaptanto/kaptanto/internal/output/sse"
	"github.com/kaptanto/kaptanto/internal/output/stdout"
	"github.com/kaptanto/kaptanto/internal/router"
	postgres "github.com/kaptanto/kaptanto/internal/source/postgres"
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
	root.PersistentFlags().Bool("ha", false, "enable high-availability mode (requires etcd)")
	root.PersistentFlags().String("node-id", "", "unique node identifier for HA mode")

	// OBS-03: Observability flags.
	root.PersistentFlags().String("log-level", "info", "log verbosity: debug | info | warn | error")

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

	// 3. Open Badger event log.
	el, err := eventlog.Open(filepath.Join(cfg.DataDir, "events"), numEventLogPartitions, retention)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer el.Close() // closed AFTER g.Wait() returns (deferred after all components stop)

	// 4. Open SQLite checkpoint store (source LSN persistence).
	ckStore, err := checkpoint.Open(filepath.Join(cfg.DataDir, "checkpoint.db"))
	if err != nil {
		return fmt.Errorf("open checkpoint store: %w", err)
	}
	defer ckStore.Close()

	// 5. Open SQLite cursor store (consumer resume cursors).
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
	cursorStore, err := checkpoint.NewSQLiteCursorStore(cursorDB, 5*time.Second)
	if err != nil {
		return fmt.Errorf("create cursor store: %w", err)
	}

	// 6. Pre-parse per-table filters (CFG-05, CFG-06). Fail fast on invalid WHERE.
	rowFilters, colFilters, err := buildTableFilters(cfg.Tables)
	if err != nil {
		return err
	}

	// 7. Create router.
	rtr := router.NewRouter(el, numEventLogPartitions, cursorStore)

	// 8. Create observability (metrics + health).
	metrics := observability.NewKaptantoMetrics()
	healthHandler := observability.NewHealthHandler([]observability.HealthProbe{})

	// 9. Wire output — register consumer(s) BEFORE starting the router.
	var outputServer func(ctx context.Context) error
	switch cfg.Output {
	case "stdout":
		writer := stdout.NewStdoutWriter(os.Stdout)
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
				_ = srv.Shutdown(context.Background())
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
				_ = obsSrv.Shutdown(context.Background())
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

	// 10. Build Postgres connector (backfill engine nil for Phase 7.2).
	tables := make([]string, 0, len(cfg.Tables))
	for t := range cfg.Tables {
		tables = append(tables, t)
	}
	connCfg := postgres.Config{
		DSN:      cfg.Source,
		Tables:   tables,
		SourceID: "default",
	}
	connCfg.ApplyDefaults()
	idGen := event.NewIDGenerator()
	connector := postgres.NewWithBackfill(connCfg, ckStore, idGen, el, nil)

	// 11. Run all components under errgroup — first error cancels the group context.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { cursorStore.Run(gctx); return nil })
	g.Go(func() error { return connector.Run(gctx) })
	g.Go(func() error { return rtr.Run(gctx) })
	g.Go(func() error { return outputServer(gctx) })

	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}
	slog.Info("kaptanto shut down cleanly")
	return nil
}
