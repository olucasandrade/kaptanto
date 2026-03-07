// Package cmd implements the kaptanto CLI using cobra.
package cmd

import (
	"io"
	"os"
	"time"

	"github.com/kaptanto/kaptanto/internal/logging"
	"github.com/spf13/cobra"
)

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
		// RunE is a no-op placeholder so cobra prints full usage (including flags) when
		// no subcommand is provided. Future phases will replace this with real behavior.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
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

// Ensure time package is used (duration flag uses time.Duration).
var _ time.Duration
