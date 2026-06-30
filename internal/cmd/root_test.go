package cmd_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olucasandrade/kaptanto/internal/cmd"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/logging"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEventLogForCmd is a minimal eventlog.EventLog implementation used as a
// compile guard in TestRouterSetOwnedPartitions.
type fakeEventLogForCmd struct{}

func (f *fakeEventLogForCmd) Append(_ *event.ChangeEvent) (uint64, error) { return 0, nil }
func (f *fakeEventLogForCmd) AppendBatch(_ []*event.ChangeEvent) ([]uint64, error) {
	return nil, nil
}
func (f *fakeEventLogForCmd) ReadPartition(_ context.Context, _ uint32, _ uint64, _ int) ([]eventlog.LogEntry, error) {
	return nil, nil
}
func (f *fakeEventLogForCmd) Close() error { return nil }

// lockedBuffer is a goroutine-safe bytes.Buffer, used when a background
// pipeline writes log output concurrently with the test reading it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestHelpContainsKaptanto(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--help"}, buf)
	// --help returns an error from cobra (pflag.ErrHelp) but that's expected;
	// what matters is output contains "kaptanto"
	_ = err
	out := buf.String()
	assert.Contains(t, out, "kaptanto", "help output should contain 'kaptanto'")
}

func TestHelpContainsAllFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	_ = cmd.ExecuteWithArgs([]string{"--help"}, buf)
	out := buf.String()

	flags := []string{
		"--source",
		"--tables",
		"--output",
		"--port",
		"--config",
		"--data-dir",
		"--retention",
		"--ha",
		"--node-id",
		"--log-level",
	}
	for _, f := range flags {
		assert.Contains(t, out, f, "help output should contain flag %s", f)
	}
}

func TestFlagSource(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("source")
	require.NotNil(t, f, "flag 'source' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagTables(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("tables")
	require.NotNil(t, f, "flag 'tables' must exist")
	assert.Equal(t, "stringArray", f.Value.Type())
}

func TestFlagOutput(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("output")
	require.NotNil(t, f, "flag 'output' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "stdout", f.DefValue)
}

func TestFlagPort(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("port")
	require.NotNil(t, f, "flag 'port' must exist")
	assert.Equal(t, "int", f.Value.Type())
	assert.Equal(t, "7654", f.DefValue)
}

func TestFlagConfig(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("config")
	require.NotNil(t, f, "flag 'config' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagDataDir(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("data-dir")
	require.NotNil(t, f, "flag 'data-dir' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "./data", f.DefValue)
}

func TestFlagRetention(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("retention")
	require.NotNil(t, f, "flag 'retention' must exist")
	assert.Equal(t, "duration", f.Value.Type())
	// default is 0 (no retention limit applied at flag layer)
	assert.Equal(t, "0s", f.DefValue)
}

func TestFlagHA(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("ha")
	require.NotNil(t, f, "flag 'ha' must exist")
	assert.Equal(t, "bool", f.Value.Type())
	assert.Equal(t, "false", f.DefValue)
}

func TestFlagNodeID(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("node-id")
	require.NotNil(t, f, "flag 'node-id' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "", f.DefValue)
}

func TestFlagLogLevel(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("log-level")
	require.NotNil(t, f, "flag 'log-level' must exist")
	assert.Equal(t, "string", f.Value.Type())
	assert.Equal(t, "info", f.DefValue)
}

func TestRetentionFlagType(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("retention")
	require.NotNil(t, f)
	// Parse a duration value to verify the flag accepts durations
	err := f.Value.Set("1h")
	require.NoError(t, err)
	v, err := time.ParseDuration(f.Value.String())
	require.NoError(t, err)
	assert.Equal(t, time.Hour, v)
}

// TestRunE_MissingSourceAndConfig verifies the guard condition: when neither
// --source nor --config is provided, RunE returns an error containing
// "--source or --config is required".
func TestRunE_MissingSourceAndConfig(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs(nil, buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source or --config is required")
}

// TestRunE_EmptySource verifies that explicitly passing an empty --source is
// treated as not set (the guard catches it).
func TestRunE_EmptySource(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--source", ""}, buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source or --config is required")
}

// TestRunE_ConfigFileNotFound verifies that --config pointing to a non-existent
// file returns an error (load config failure).
func TestRunE_ConfigFileNotFound(t *testing.T) {
	buf := &bytes.Buffer{}
	err := cmd.ExecuteWithArgs([]string{"--config", "/tmp/nonexistent_kaptanto_test.yaml"}, buf)
	require.Error(t, err)
}

// TestFlagOutputUsageComplete verifies the --output flag description lists all 8
// valid output modes. Closes CFG-04 gap: kafka, pubsub, rabbitmq were missing.
func TestFlagOutputUsageComplete(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("output")
	require.NotNil(t, f, "flag 'output' must exist")
	for _, mode := range []string{"stdout", "sse", "grpc", "nats", "sqs", "kafka", "pubsub", "rabbitmq"} {
		assert.Contains(t, f.Usage, mode, "--output help text must contain %q", mode)
	}
}

// TestHAFlagHelpText verifies the --ha flag description references "advisory lock"
// and no longer references the outdated "etcd" mechanism.
func TestHAFlagHelpText(t *testing.T) {
	root := cmd.NewRootCmd()
	f := root.PersistentFlags().Lookup("ha")
	require.NotNil(t, f, "flag 'ha' must exist")
	assert.NotContains(t, f.Usage, "etcd", "--ha help text must not mention etcd")
	assert.Contains(t, f.Usage, "advisory lock", "--ha help text must mention advisory lock")
}

// TestNonHAPathUnchanged verifies that running without --ha does not emit any
// "ha:" log lines, confirming the non-HA pipeline path is unaffected.
func TestNonHAPathUnchanged(t *testing.T) {
	// Skip if no POSTGRES_TEST_DSN — this test would start a real pipeline.
	if os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Skip("POSTGRES_TEST_DSN not set; skipping non-HA path test")
	}

	// Logging is configured via logging.Setup(os.Stderr, ...) in the command's
	// PersistentPreRunE, so cobra's SetOut/SetErr do NOT capture slog output.
	// Redirect os.Stderr to a pipe for the duration to capture the real logs.
	origStderr := os.Stderr
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = pw
	logs := &lockedBuffer{}
	copyDone := make(chan struct{})
	go func() { _, _ = io.Copy(logs, pr); close(copyDone) }()

	// When POSTGRES_TEST_DSN points at a reachable Postgres the non-HA pipeline
	// streams indefinitely and its graceful shutdown may not return promptly, so
	// we do NOT wait for ExecuteContext to return — run it in the background.
	ctx, cancel := context.WithCancel(context.Background())

	// Teardown runs unconditionally (even if a require below fails), so we never
	// leave os.Stderr or the process-wide slog default pointed at a closed pipe
	// fd, which would corrupt later tests. logging.Setup re-routes the global
	// logger back to the real stderr through the same path PersistentPreRunE uses.
	t.Cleanup(func() {
		cancel()
		os.Stderr = origStderr
		logging.Setup(origStderr, "info")
		_ = pw.Close()
		<-copyDone
	})

	root := cmd.NewRootCmd()
	root.SetArgs([]string{
		"--source", os.Getenv("POSTGRES_TEST_DSN"),
		"--data-dir", t.TempDir(),
	})
	go func() { _ = root.ExecuteContext(ctx) }()

	// Wait until the pipeline logs its startup line — this proves the non-HA
	// path actually started (so the "ha:" check below is meaningful), and is
	// bounded so a failure to start fails fast rather than hanging.
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "kaptanto starting")
	}, 15*time.Second, 100*time.Millisecond, "non-HA pipeline did not start")

	out := logs.String()
	assert.False(t, strings.Contains(out, "ha:"), "non-HA path must not emit ha: log lines; got: %s", out)
}

// TestMongoDBFlagRoute verifies that when --source starts with "mongodb://",
// runPipeline takes the MongoDB branch (not the Postgres branch). The test
// confirms this by checking the error is a connection error, not the
// "source is required" guard error.
//
// serverSelectionTimeoutMS=500 makes the MongoDB driver fail fast when the
// server is unreachable, so the test completes in under 2s.
func TestMongoDBFlagRoute(t *testing.T) {
	// Use an unreachable MongoDB URI so the pipeline fails at connect time,
	// proving the MongoDB branch was entered (not the Postgres branch).
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "mongodb://127.0.0.1:27999/testdb?serverSelectionTimeoutMS=500",
		"--tables", "testcoll",
	}, &buf)
	// Must return an error (connect failure), NOT the "source is required" guard.
	require.Error(t, err, "mongodb pipeline must fail (no server at 27999)")
	assert.NotContains(t, err.Error(), "--source or --config is required",
		"error must be a connect failure, not the source guard")
	assert.NotContains(t, err.Error(), "source is required",
		"error must be a connect failure, not the source guard")
}

// TestMongoDBWithHAReturnsError verifies that passing --ha with a MongoDB
// source DSN returns a clear error before any Postgres connection is attempted.
// INT-03 gap closure.
func TestMongoDBWithHAReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "mongodb://127.0.0.1:27999/testdb",
		"--ha",
		"--tables", "testcoll",
	}, &buf)
	require.Error(t, err, "mongodb + --ha must return an error")
	assert.Contains(t, err.Error(), "ha:", "error must be prefixed ha:")
	assert.Contains(t, err.Error(), "Postgres", "error must mention Postgres requirement")
	assert.Contains(t, err.Error(), "mongodb", "error must mention the detected source type")
}

// TestHAFlagSkipsWithoutDSN verifies that --ha without POSTGRES_TEST_DSN
// causes the pipeline to return an error (connection failure), not a panic.
// This is an integration guard: when no Postgres is available the HA path
// should fail gracefully.
func TestHAFlagSkipsWithoutDSN(t *testing.T) {
	if os.Getenv("POSTGRES_TEST_DSN") != "" {
		t.Skip("POSTGRES_TEST_DSN is set; skipping graceful-skip test")
	}
	// Use an obviously unreachable DSN to trigger a connect error.
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--ha",
	}, &buf)
	// The HA path should return an error from NewLeaderElector or OpenPostgres,
	// not panic and not emit the old slog.Warn stub.
	require.Error(t, err, "HA mode with unreachable DB must return an error")
	assert.NotContains(t, err.Error(), "not yet implemented", "old slog.Warn stub must be gone")
}

// TestClusterFlagRegistered verifies that --cluster and --cluster-dsn flags are
// registered with the correct types and default values.
func TestClusterFlagRegistered(t *testing.T) {
	root := cmd.NewRootCmd()

	clusterFlag := root.PersistentFlags().Lookup("cluster")
	require.NotNil(t, clusterFlag, "flag 'cluster' must exist")
	assert.Equal(t, "bool", clusterFlag.Value.Type())
	assert.Equal(t, "false", clusterFlag.DefValue)

	clusterDSNFlag := root.PersistentFlags().Lookup("cluster-dsn")
	require.NotNil(t, clusterDSNFlag, "flag 'cluster-dsn' must exist")
	assert.Equal(t, "string", clusterDSNFlag.Value.Type())
	assert.Equal(t, "", clusterDSNFlag.DefValue)
}

// TestClusterWithoutDSNReturnsError verifies that --cluster without --cluster-dsn
// returns a clear error before any connection is attempted.
func TestClusterWithoutDSNReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--cluster",
	}, &buf)
	require.Error(t, err, "--cluster without --cluster-dsn must return an error")
	assert.Contains(t, err.Error(), "--cluster-dsn is required when --cluster is set")
}

// TestOutputMode_Nats_MissingConfig verifies that running --output nats without a
// sinks.nats block in config returns an error containing "sinks.nats".
// No NATS server is required — this exercises the nil-config guard.
func TestOutputMode_Nats_MissingConfig(t *testing.T) {
	var buf bytes.Buffer
	// Use an unreachable Postgres DSN so the pipeline reaches the output switch
	// before failing. The nil sinks.nats guard fires before any DB connection.
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "nats",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output nats without sinks.nats config must return an error")
	assert.Contains(t, err.Error(), "sinks.nats",
		"error must mention sinks.nats config block")
}

// TestOutputMode_Nats_InvalidMode verifies that --output with an unknown mode
// returns an error message that includes "nats" in the list of valid modes.
func TestOutputMode_Nats_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "invalid-queue",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output invalid-queue must return an error")
	assert.Contains(t, err.Error(), "nats",
		"error must include 'nats' in valid output modes list")
}

// TestOutputMode_SQS_MissingConfig verifies that running --output sqs without a
// sinks.sqs block in config returns an error containing "sinks.sqs".
// No AWS connection is required — this exercises the nil-config guard only.
func TestOutputMode_SQS_MissingConfig(t *testing.T) {
	var buf bytes.Buffer
	// Use an unreachable Postgres DSN so the pipeline reaches the output switch
	// before failing. The nil sinks.sqs guard fires before any DB connection.
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "sqs",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output sqs without sinks.sqs config must return an error")
	assert.Contains(t, err.Error(), "sinks.sqs",
		"error must mention sinks.sqs config block")
}

// TestOutputMode_SQS_InvalidMode verifies that --output with an unknown mode
// returns an error message that includes "sqs" in the list of valid modes.
func TestOutputMode_SQS_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "invalid-queue-mode",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output invalid-queue-mode must return an error")
	assert.Contains(t, err.Error(), "sqs",
		"error must include 'sqs' in valid output modes list")
}

// TestOutputMode_Kafka_MissingConfig verifies that running --output kafka without a
// sinks.kafka block in config returns an error containing "sinks.kafka".
// No Kafka broker connection is required — this exercises the nil-config guard only.
func TestOutputMode_Kafka_MissingConfig(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "kafka",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output kafka without sinks.kafka config must return an error")
	assert.Contains(t, err.Error(), "sinks.kafka",
		"error must mention sinks.kafka config block")
}

// TestOutputMode_Kafka_InvalidMode verifies that --output with an unknown mode
// returns an error message that includes "kafka" in the list of valid modes.
func TestOutputMode_Kafka_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "invalid-kafka-mode",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output invalid-kafka-mode must return an error")
	assert.Contains(t, err.Error(), "kafka",
		"error must include 'kafka' in valid output modes list")
}

// TestOutputMode_PubSub_MissingConfig verifies that running --output pubsub without a
// sinks.pubsub block in config returns an error containing "sinks.pubsub".
// No GCP connection is required — this exercises the nil-config guard only.
func TestOutputMode_PubSub_MissingConfig(t *testing.T) {
	var buf bytes.Buffer
	// Use an unreachable Postgres DSN so the pipeline reaches the output switch
	// before failing. The nil sinks.pubsub guard fires before any DB connection.
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "pubsub",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output pubsub without sinks.pubsub config must return an error")
	assert.Contains(t, err.Error(), "sinks.pubsub",
		"error must mention sinks.pubsub config block")
}

// TestOutputMode_PubSub_InvalidMode verifies that --output with an unknown mode
// returns an error message that includes "pubsub" in the list of valid modes.
func TestOutputMode_PubSub_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "invalid-pubsub-mode",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output invalid-pubsub-mode must return an error")
	assert.Contains(t, err.Error(), "pubsub",
		"error must include 'pubsub' in valid output modes list")
}

// TestOutputMode_RabbitMQ_MissingConfig verifies that running --output rabbitmq without a
// sinks.rabbitmq block in config returns an error containing "sinks.rabbitmq".
// No RabbitMQ connection is required — this exercises the nil-config guard only.
func TestOutputMode_RabbitMQ_MissingConfig(t *testing.T) {
	var buf bytes.Buffer
	// Use an unreachable Postgres DSN so the pipeline reaches the output switch
	// before failing. The nil sinks.rabbitmq guard fires before any DB connection.
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "rabbitmq",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output rabbitmq without sinks.rabbitmq config must return an error")
	assert.Contains(t, err.Error(), "sinks.rabbitmq",
		"error must mention sinks.rabbitmq config block")
}

// TestOutputMode_RabbitMQ_InvalidMode verifies that --output with an unknown mode
// returns an error message that includes "rabbitmq" in the list of valid modes.
func TestOutputMode_RabbitMQ_InvalidMode(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://kaptanto_test:kaptanto_test@127.0.0.1:54321/kaptanto_test",
		"--output", "invalid-rabbitmq-mode",
		"--all-tables",
	}, &buf)
	require.Error(t, err, "--output invalid-rabbitmq-mode must return an error")
	assert.Contains(t, err.Error(), "rabbitmq",
		"error must include 'rabbitmq' in valid output modes list")
}

// TestAllTables_FailClosedWithNoTables verifies that a Postgres source with no
// tables configured and no --all-tables flag returns a descriptive error and does
// NOT proceed to attempt a database connection.
func TestAllTables_FailClosedWithNoTables(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://user:pass@127.0.0.1:54321/db",
		// no --tables, no --all-tables
	}, &buf)
	require.Error(t, err, "empty tables without --all-tables must fail fast")
	assert.Contains(t, err.Error(), "--all-tables",
		"error must mention --all-tables opt-in")
	assert.Contains(t, err.Error(), "tables",
		"error must mention tables configuration")
}

// TestAllTables_MongoDBSourceNotBlocked verifies that MongoDB sources are not
// affected by the tables guard (MongoDB uses Collection configs, not table lists).
func TestAllTables_MongoDBSourceNotBlocked(t *testing.T) {
	var buf bytes.Buffer
	err := cmd.ExecuteWithArgs([]string{
		"--source", "mongodb://user:pass@127.0.0.1:27017/db",
		// no --tables, no --all-tables — MongoDB path should not be blocked
	}, &buf)
	// We expect an error (no real MongoDB), but it must NOT be the tables guard.
	if err != nil {
		assert.NotContains(t, err.Error(), "--all-tables",
			"MongoDB source must not be blocked by the all-tables guard")
	}
}

// TestAllTables_FlagMergedIntoConfig verifies that --all-tables is correctly
// parsed and merged into the Config struct.
func TestAllTables_FlagMergedIntoConfig(t *testing.T) {
	var buf bytes.Buffer
	// With --all-tables set, the guard should pass and we reach the next failure
	// (sink config / output mode), not the tables guard.
	err := cmd.ExecuteWithArgs([]string{
		"--source", "postgres://user:pass@127.0.0.1:54321/db",
		"--all-tables",
		"--output", "invalid-mode-xyz",
	}, &buf)
	require.Error(t, err, "invalid output mode must still produce an error")
	assert.NotContains(t, err.Error(), "--all-tables",
		"with --all-tables set the tables guard must not fire")
}

// TestRouterSetOwnedPartitions is a compile guard: if SetOwnedPartitions is
// removed or its signature changes, this test will fail to compile.
// Uses the same fakeEventLog pattern as internal/router/router_test.go.
func TestRouterSetOwnedPartitions(t *testing.T) {
	el := &fakeEventLogForCmd{}
	rtr := router.NewRouter(el, 64, nil)
	rtr.SetOwnedPartitions([]uint32{0, 1, 2})
	rtr.SetOwnedPartitions(nil)
	_ = rtr
}
