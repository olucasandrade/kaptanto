package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/kaptanto/kaptanto/internal/backfill"
	"github.com/kaptanto/kaptanto/internal/checkpoint"
	"github.com/kaptanto/kaptanto/internal/event"
	"github.com/kaptanto/kaptanto/internal/eventlog"
	"github.com/kaptanto/kaptanto/internal/parser/pgoutput"
)

const (
	defaultInitialBackoff  = 2 * time.Second
	defaultMaxBackoff      = 60 * time.Second
	defaultStandbyTimeout  = 10 * time.Second
	defaultWALLagThreshold = 100 * 1024 * 1024 // 100 MB
	walLagCheckInterval    = 30 * time.Second
)

// Config holds all parameters for the PostgresConnector.
type Config struct {
	// DSN is the libpq-style connection string. Supports multi-host syntax:
	//   "postgres://h1,h2/db?target_session_attrs=read-write"
	DSN string

	// SlotName is the name of the logical replication slot.
	// Defaults to "kaptanto_" + SourceID when empty.
	SlotName string

	// PublicationName is the name of the Postgres publication.
	// Defaults to "kaptanto_pub_" + SourceID when empty.
	PublicationName string

	// Tables lists tables to include in the publication (schema.table format).
	// If empty, the publication is created FOR ALL TABLES.
	Tables []string

	// SourceID is a stable identifier for this source in idempotency keys
	// and checkpoint records.
	SourceID string

	// InitialBackoff is the starting reconnect delay. Defaults to 2s.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum reconnect delay. Defaults to 60s.
	MaxBackoff time.Duration

	// StandbyTimeout is the interval between standby status update heartbeats.
	// Defaults to 10s.
	StandbyTimeout time.Duration

	// WALLagThreshold is the byte threshold above which a WAL lag warning is
	// emitted. Defaults to 100 MB.
	WALLagThreshold int64
}

// ApplyDefaults fills in zero-value Config fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.SlotName == "" {
		c.SlotName = "kaptanto_" + c.SourceID
	}
	if c.PublicationName == "" {
		c.PublicationName = "kaptanto_pub_" + c.SourceID
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = defaultInitialBackoff
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.StandbyTimeout == 0 {
		c.StandbyTimeout = defaultStandbyTimeout
	}
	if c.WALLagThreshold == 0 {
		c.WALLagThreshold = defaultWALLagThreshold
	}
}

// BuildReplicationDSN appends the "replication=database" parameter to the
// provided DSN, correctly handling whether the DSN already has a "?" query
// separator or not.
func BuildReplicationDSN(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&replication=database"
	}
	return dsn + "?replication=database"
}

// EvalSlotCheck encodes the SRC-06 logic for determining whether a snapshot
// is needed after a slot goes missing:
//   - slotPresent=true → needsSnapshot=false (slot exists, no problem)
//   - slotPresent=false, wasEverConnected=false → needsSnapshot=false (first run)
//   - slotPresent=false, wasEverConnected=true  → needsSnapshot=true (failover gap)
func EvalSlotCheck(slotPresent, wasEverConnected bool) (needsSnapshot bool) {
	if slotPresent {
		return false
	}
	return wasEverConnected
}

// PostgresConnector implements the Postgres CDC source. It maintains a
// replication connection (for WAL streaming) and a separate query connection
// (for schema queries, slot/publication management, and lag monitoring).
//
// The connector emits decoded *event.ChangeEvent values on the channel
// returned by Events().
type PostgresConnector struct {
	cfg         Config
	store       checkpoint.CheckpointStore
	idGen       *event.IDGenerator
	parser      *pgoutput.Parser
	events      chan *event.ChangeEvent
	eventLog    eventlog.EventLog
	backfillEng backfill.BackfillEngine
	// appendMu serializes eventLog.Append calls. Both the WAL goroutine and
	// the backfill goroutine call AppendAndQueue concurrently; without this
	// mutex, concurrent Append calls to BadgerDB would race (Pitfall 2).
	appendMu sync.Mutex
}

// New creates a PostgresConnector without an EventLog. Call Run(ctx) to start
// streaming. When eventLog is nil the connector skips Append (backward-compatible
// for callers that do not need the event log yet).
func New(cfg Config, store checkpoint.CheckpointStore, idGen *event.IDGenerator) *PostgresConnector {
	return NewWithEventLog(cfg, store, idGen, nil)
}

// NewWithEventLog creates a PostgresConnector with a durable EventLog. Every
// ChangeEvent parsed from WAL will be passed to el.Append before being queued
// on the events channel and before the source LSN is acknowledged to Postgres
// (LOG-01 + CHK-01 ordering).
//
// If el is nil, Append is skipped — equivalent to calling New.
func NewWithEventLog(cfg Config, store checkpoint.CheckpointStore, idGen *event.IDGenerator, el eventlog.EventLog) *PostgresConnector {
	cfg.ApplyDefaults()
	p := pgoutput.New(cfg.SourceID, idGen)
	return &PostgresConnector{
		cfg:      cfg,
		store:    store,
		idGen:    idGen,
		parser:   p,
		events:   make(chan *event.ChangeEvent, 1024),
		eventLog: el,
	}
}

// NewWithBackfill creates a PostgresConnector with a durable EventLog and a
// BackfillEngine. The backfill engine is started after WAL replication begins
// (in connectAndStream) when HasPendingBackfills() returns true.
//
// If bf is nil, the connector behaves identically to NewWithEventLog.
func NewWithBackfill(
	cfg Config,
	store checkpoint.CheckpointStore,
	idGen *event.IDGenerator,
	el eventlog.EventLog,
	bf backfill.BackfillEngine,
) *PostgresConnector {
	c := NewWithEventLog(cfg, store, idGen, el)
	c.backfillEng = bf
	return c
}

// EventLog returns the EventLog wired into this connector, or nil if none was
// provided. Exposed for testing.
func (c *PostgresConnector) EventLog() eventlog.EventLog {
	return c.eventLog
}

// SetBackfillEngine injects a BackfillEngine after construction. This breaks the
// circular dependency where the engine needs connector.AppendAndQueue as its
// appendFn, but the connector constructor needs the engine. Pattern mirrors
// BackfillEngineImpl.SetWatermark.
func (c *PostgresConnector) SetBackfillEngine(eng backfill.BackfillEngine) {
	c.backfillEng = eng
}

// AppendAndQueue durably appends ev to the event log (if configured) and then
// forwards ev to the events channel. If Append fails, the error is returned
// immediately and ev is NOT sent to the channel — the connector must not
// advance the checkpoint in that case (CHK-01).
//
// When eventLog is nil, AppendAndQueue skips Append and forwards ev directly
// (backward-compatible nil guard for Phase 4/5 wiring).
func (c *PostgresConnector) AppendAndQueue(ctx context.Context, ev *event.ChangeEvent) error {
	if c.eventLog != nil {
		// LOG-01: event written durably before checkpoint is advanced (CHK-01 ordering).
		// appendMu serializes concurrent Append calls from WAL goroutine and backfill goroutine.
		c.appendMu.Lock()
		_, err := c.eventLog.Append(ev)
		c.appendMu.Unlock()
		if err != nil {
			return fmt.Errorf("eventlog: append: %w", err)
		}
	}
	select {
	case c.events <- ev:
	default:
		// Router reads from eventLog.ReadPartition, not this channel.
		// Drop is safe; the event is already durably written to the event log.
	}
	return nil
}

// Events returns the read-only channel on which ChangeEvents are emitted.
// Callers should range over this channel concurrently with Run.
func (c *PostgresConnector) Events() <-chan *event.ChangeEvent {
	return c.events
}

// Run starts the outer reconnect loop. It returns when ctx is cancelled.
// Connection errors trigger exponential backoff (InitialBackoff → MaxBackoff).
func (c *PostgresConnector) Run(ctx context.Context) error {
	backoff := c.cfg.InitialBackoff
	wasEverConnected := false

	for {
		err := c.connectAndStream(ctx, wasEverConnected)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wasEverConnected = true
		slog.Warn("postgres connector disconnected, reconnecting",
			"error", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < c.cfg.MaxBackoff {
			backoff = backoff * 2
			if backoff > c.cfg.MaxBackoff {
				backoff = c.cfg.MaxBackoff
			}
		}
	}
}

// connectAndStream opens both connections, ensures the slot and publication
// exist, starts logical replication, and runs the ReceiveMessage loop until
// an error or context cancellation.
func (c *PostgresConnector) connectAndStream(ctx context.Context, wasEverConnected bool) error {
	// 1. Open replication connection (pgx/v5/pgconn, same package pglogrepl uses).
	replConn, err := pgconn.Connect(ctx, BuildReplicationDSN(c.cfg.DSN))
	if err != nil {
		return fmt.Errorf("postgres: open replication connection: %w", err)
	}
	defer func() { _ = replConn.Close(ctx) }()

	// 2. Open query connection (pgx.Conn — never mixed with replication).
	queryConn, err := pgx.Connect(ctx, c.cfg.DSN)
	if err != nil {
		return fmt.Errorf("postgres: open query connection: %w", err)
	}
	defer func() { _ = queryConn.Close(ctx) }()

	// 3. Verify this is a primary (SRC-05).
	if err := checkPrimary(ctx, queryConn); err != nil {
		return err
	}

	// 4. Check REPLICA IDENTITY for each configured table (SRC-08).
	for _, t := range c.cfg.Tables {
		schema, table := splitSchemaTable(t)
		if err := checkReplicaIdentity(ctx, queryConn, schema, table); err != nil {
			return err
		}
	}

	// 5. Ensure publication exists (SRC-02).
	if err := ensurePublication(ctx, queryConn, c.cfg.PublicationName, c.cfg.Tables); err != nil {
		return err
	}

	// 6. Ensure slot exists; detect missing slot after failover (SRC-06).
	needsSnapshot, _, err := ensureSlot(ctx, replConn, queryConn, c.cfg.SlotName, wasEverConnected)
	if err != nil {
		return err
	}
	if needsSnapshot {
		slog.Warn("postgres: replication slot was absent after reconnect — a snapshot backfill will be needed",
			"slot", c.cfg.SlotName)
	}

	// 7. Load last LSN from checkpoint store.
	lastLSNStr, err := c.store.Load(ctx, c.cfg.SourceID)
	if err != nil {
		return fmt.Errorf("postgres: load checkpoint: %w", err)
	}

	// 8. Parse stored LSN; start from 0 on first run.
	var startLSN pglogrepl.LSN
	if lastLSNStr != "" {
		startLSN, err = pglogrepl.ParseLSN(lastLSNStr)
		if err != nil {
			return fmt.Errorf("postgres: parse checkpoint LSN %q: %w", lastLSNStr, err)
		}
		// 9. Advance by 1 to avoid re-delivering the last event (off-by-one).
		startLSN++
	}

	// 10. Clear relation cache — new session; Postgres will re-send RelationMessages.
	c.parser.ClearRelationCache()

	// 11. Start logical replication (SRC-01).
	if err := pglogrepl.StartReplication(ctx, replConn, c.cfg.SlotName, startLSN,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '2'",
				fmt.Sprintf("publication_names '%s'", c.cfg.PublicationName),
				"messages 'true'",
				"streaming 'true'",
			},
		},
	); err != nil {
		return fmt.Errorf("postgres: start replication: %w", err)
	}

	// 12a. Launch backfill goroutine if backfill engine has pending work.
	// The backfill engine delivers events via AppendAndQueue (same path as WAL events),
	// which serializes Append calls via appendMu — no concurrent BadgerDB writes (Pitfall 2).
	if c.backfillEng != nil && c.backfillEng.HasPendingBackfills() {
		go func() {
			if err := c.backfillEng.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("backfill engine: run failed", "error", err)
			}
		}()
	}

	// 12b. SRC-06: if the slot was absent after reconnect, the WAL gap means
	// existing data may be missed — trigger a full re-snapshot via the backfill engine.
	// Guard: placed after StartReplication (line 308) so the slot and publication
	// are confirmed present before snapshot queries begin.
	if needsSnapshot && c.backfillEng != nil {
		go func() {
			if err := c.backfillEng.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("backfill engine: re-snapshot after slot loss failed", "error", err)
			}
		}()
	}

	// 13. WAL lag monitoring goroutine (SRC-07).
	lagCtx, cancelLag := context.WithCancel(ctx)
	defer cancelLag()
	go func() {
		ticker := time.NewTicker(walLagCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-lagCtx.Done():
				return
			case <-ticker.C:
				_ = checkWALLag(lagCtx, queryConn, c.cfg.WALLagThreshold)
			}
		}
	}()

	// 12. ReceiveMessage loop (SRC-03).
	return c.receiveLoop(ctx, replConn)
}

// receiveLoop runs the core WAL message receive loop using pgconn.ReceiveMessage
// and pglogrepl parsers. It sends standby status updates on heartbeat timeout
// and when PrimaryKeepalive.ReplyRequested is set.
func (c *PostgresConnector) receiveLoop(
	ctx context.Context,
	replConn *pgconn.PgConn,
) error {
	var clientXLogPos pglogrepl.LSN
	nextHeartbeat := time.Now().Add(c.cfg.StandbyTimeout)

	for {
		// Set receive deadline to next heartbeat.
		recvCtx, cancel := context.WithDeadline(ctx, nextHeartbeat)
		rawMsg, err := replConn.ReceiveMessage(recvCtx)
		cancel()

		if err != nil {
			if pgconn.Timeout(err) {
				// Heartbeat deadline exceeded — send standby status update (SRC-03).
				if sendErr := c.sendStandbyStatus(ctx, replConn, clientXLogPos); sendErr != nil {
					return fmt.Errorf("postgres: send standby heartbeat: %w", sendErr)
				}
				nextHeartbeat = time.Now().Add(c.cfg.StandbyTimeout)
				continue
			}
			// Context cancelled — graceful shutdown.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("postgres: receive message: %w", err)
		}

		copyData, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			// ErrorResponse or other backend message — return as error.
			return fmt.Errorf("postgres: unexpected message type %T", rawMsg)
		}

		if len(copyData.Data) == 0 {
			continue
		}

		switch copyData.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(copyData.Data[1:])
			if err != nil {
				slog.Warn("postgres: parse keepalive message", "error", err)
				continue
			}
			if pkm.ServerWALEnd > clientXLogPos {
				clientXLogPos = pkm.ServerWALEnd
			}
			// If server requests a reply, send status immediately (SRC-03).
			if pkm.ReplyRequested {
				if sendErr := c.sendStandbyStatus(ctx, replConn, clientXLogPos); sendErr != nil {
					return fmt.Errorf("postgres: send standby on request: %w", sendErr)
				}
				nextHeartbeat = time.Now().Add(c.cfg.StandbyTimeout)
			}

		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(copyData.Data[1:])
			if err != nil {
				slog.Warn("postgres: parse XLogData", "error", err)
				continue
			}

			ev, parseErr := c.parser.Parse(xld.WALData, false)
			if parseErr != nil {
				slog.Warn("postgres: parse WAL data error", "error", parseErr)
				// Non-fatal: log and continue (unknown RelationID can occur transiently).
				break
			}
			if ev != nil {
				// LOG-01: event written durably before checkpoint is advanced (CHK-01 ordering).
				if err := c.AppendAndQueue(ctx, ev); err != nil {
					return err
				}
			}

			// Advance client position.
			end := xld.WALStart + pglogrepl.LSN(len(xld.WALData))
			if end > clientXLogPos {
				clientXLogPos = end
			}

			// CHK-01: save checkpoint BEFORE advancing LSN to Postgres.
			// When the parser emits nil (Commit message), we persist the LSN.
			// We detect "commit" by checking if WALData[0] == 'C' (CommitMessage).
			if len(xld.WALData) > 0 && xld.WALData[0] == 'C' {
				lsnStr := clientXLogPos.String()
				if saveErr := c.store.Save(ctx, c.cfg.SourceID, lsnStr); saveErr != nil {
					return fmt.Errorf("postgres: save checkpoint: %w", saveErr)
				}
				// CHK-01: checkpoint BEFORE advancing LSN to Postgres.
				if sendErr := c.sendStandbyStatus(ctx, replConn, clientXLogPos); sendErr != nil {
					return fmt.Errorf("postgres: send standby after commit: %w", sendErr)
				}
				nextHeartbeat = time.Now().Add(c.cfg.StandbyTimeout)
			}
		}
	}
}

// sendStandbyStatus sends a StandbyStatusUpdate to the server.
// CRITICAL: This must only be called AFTER store.Save() on commit (CHK-01).
func (c *PostgresConnector) sendStandbyStatus(ctx context.Context, conn *pgconn.PgConn, pos pglogrepl.LSN) error {
	return pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: pos,
		ReplyRequested:   false,
	})
}
