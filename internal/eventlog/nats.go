// Package eventlog provides a NATS JetStream-backed implementation of EventLog
// for cluster mode. NatsEventLog replaces BadgerEventLog when kaptanto runs with
// --cluster, providing Raft-replicated durability so a single node crash cannot
// lose events already acknowledged to the source connector (EVLOG-01).
//
// CHK-01 holds cluster-wide: Append blocks until a quorum of NATS JetStream nodes
// confirms the write via a synchronous PubAck (EVLOG-02). With Replicas=3 and
// SyncAlways=true on the embedded server, PubAck is not returned until 2-of-3 nodes
// have fsynced the message.
//
// Deduplication is handled by the NATS server via the Nats-Msg-Id header and the
// StreamConfig.Duplicates window (set to retention duration, matching Badger semantics).
// Duplicate publishes return PubAck.Duplicate=true with err=nil — NOT an error (Pitfall 3).
//
// Partitioning: 64 JetStream subjects (kaptanto.events.00000 … kaptanto.events.00063)
// on a single stream, using the same FNV-1a PartitionOf function as BadgerEventLog
// to preserve backward compatibility with WatermarkChecker and Router (BKF-02).
package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	natssrv "github.com/nats-io/nats-server/v2/server"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// Compile-time assertion: NatsEventLog must implement EventLog.
var _ EventLog = (*NatsEventLog)(nil)

const (
	// natsStreamName is the single JetStream stream that holds all 64 partition subjects.
	// One stream per kaptanto instance — one Raft group, one SyncAlways applies.
	natsStreamName = "kaptanto-events"

	// natsSubjectPattern matches all partition subjects for use in StreamConfig.Subjects.
	natsSubjectPattern = "kaptanto.events.*"
)

// natsSubject returns the JetStream subject for the given partition number.
// Subject format: "kaptanto.events.{partition:05d}" — zero-padded for lexicographic ordering.
func natsSubject(partition uint32) string {
	return fmt.Sprintf("kaptanto.events.%05d", partition)
}

// NatsEventLogConfig holds configuration for opening a NatsEventLog.
type NatsEventLogConfig struct {
	// Server is the embedded NATS server configuration.
	Server NatsServerConfig

	// NumPartitions is the number of partitions (must match BadgerEventLog value — 64).
	// This controls the FNV-1a modulus for event routing.
	NumPartitions uint32

	// Retention is the maximum age of events in the stream (maps to Badger TTL).
	// Also sets the StreamConfig.Duplicates dedup window to prevent WAL re-delivery
	// from creating duplicates after a crash (Pitfall 2).
	Retention time.Duration
}

// NatsEventLog is the NATS JetStream-backed implementation of EventLog.
// It is safe for sequential calls from a single goroutine. Callers must
// serialize concurrent Append calls externally.
//
// Use OpenNats to construct — do not create directly.
type NatsEventLog struct {
	ns            *natssrv.Server
	nc            *nats.Conn
	js            jetstream.JetStream
	stream        jetstream.Stream
	numPartitions uint32
}

// OpenNats opens a NatsEventLog, starting an embedded NATS server and creating
// (or updating) the kaptanto-events JetStream stream.
//
// The stream Replicas value is derived from the number of configured peers:
// - No peers → R=1 (single-node or test mode)
// - N peers → R=N+1 (cluster mode; N=2 peers gives R=3 for 3-node quorum)
// This avoids the "single-node with R=3" failure where stream creation would
// block indefinitely waiting for non-existent peers to join.
func OpenNats(cfg NatsEventLogConfig) (*NatsEventLog, error) {
	ns, err := startEmbeddedNATS(cfg.Server)
	if err != nil {
		return nil, err
	}

	// On any error after server start, shut it down to avoid goroutine leaks.
	nc, err := nats.Connect(ns.ClientURL(), nats.Name("kaptanto-eventlog"))
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("nats eventlog: connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("nats eventlog: jetstream context: %w", err)
	}

	// Replicas: max(1, len(peers)+1) — single-node/test mode uses R=1;
	// cluster mode uses R=len(peers)+1 (e.g. 2 peers → R=3).
	// R=1 is the minimum JetStream accepts for stream creation.
	replicas := 1
	if n := len(cfg.Server.Peers); n > 0 {
		replicas = n + 1
	}

	retention := cfg.Retention
	if retention <= 0 {
		retention = 24 * time.Hour
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     natsStreamName,
		Subjects: []string{natsSubjectPattern},
		Replicas: replicas,
		MaxAge:   retention,
		// Duplicates: dedup window must match retention so WAL re-delivery after a crash
		// does not create duplicates if recovery takes longer than the default 2-minute window (Pitfall 2).
		Duplicates: retention,
		Storage:    jetstream.FileStorage,
	})
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("nats eventlog: create stream: %w", err)
	}

	numPartitions := cfg.NumPartitions
	if numPartitions == 0 {
		numPartitions = 64
	}

	return &NatsEventLog{
		ns:            ns,
		nc:            nc,
		js:            js,
		stream:        stream,
		numPartitions: numPartitions,
	}, nil
}

// Append durably writes ev to the JetStream stream (CHK-01).
//
// The event is published synchronously — Append blocks until the NATS server
// returns a PubAck, which (with SyncAlways=true and Replicas=3 in cluster mode)
// is not sent until a quorum of nodes has fsynced the message.
//
// Deduplication: the event's IdempotencyKey is set as the Nats-Msg-Id header.
// If a message with the same ID was previously published within the StreamConfig.Duplicates
// window, the server returns PubAck.Duplicate=true with err=nil (Pitfall 3 — NOT an error).
// In this case, Append returns seq=0 as the duplicate sentinel (LOG-03), identical to
// BadgerEventLog's behavior.
func (n *NatsEventLog) Append(ev *event.ChangeEvent) (uint64, error) {
	partition := PartitionOf(ev.Key, n.numPartitions)

	data, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("nats eventlog: marshal event: %w", err)
	}

	msg := &nats.Msg{
		Subject: natsSubject(partition),
		Data:    data,
		// Nats-Msg-Id header enables server-side deduplication within the Duplicates window.
		Header: nats.Header{nats.MsgIdHdr: []string{ev.IdempotencyKey}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ack, err := n.js.PublishMsg(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("nats eventlog: publish to partition %d: %w", partition, err)
	}

	// Duplicate detection: PubAck.Duplicate=true means the Nats-Msg-Id was seen before.
	// Return seq=0 as the "duplicate detected" sentinel (LOG-03), matching BadgerEventLog.
	if ack.Duplicate {
		return 0, nil
	}

	return ack.Sequence, nil
}

// AppendBatch durably writes all events in evs via sequential Append calls.
//
// NATS JetStream does not provide native multi-subject atomic batch transactions,
// so AppendBatch is a sequential loop. CHK-01 safety holds because each Append
// blocks until the server confirms durability before proceeding to the next event.
//
// The returned slice has the same length as evs. Duplicate events return seq=0
// at their position (LOG-03 sentinel), matching Append's contract.
func (n *NatsEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	seqs := make([]uint64, len(evs))
	for i, ev := range evs {
		seq, err := n.Append(ev)
		if err != nil {
			return nil, fmt.Errorf("nats eventlog: AppendBatch[%d]: %w", i, err)
		}
		seqs[i] = seq // 0 for duplicates (LOG-03 sentinel)
	}
	return seqs, nil
}

// ReadPartition returns up to limit events from the given partition, starting at
// fromSeq (inclusive), using a JetStream OrderedConsumer.
//
// The OrderedConsumer is created per call (stateless, no persistent subscription).
// It uses DeliverByStartSequencePolicy with OptStartSeq=fromSeq, which tells
// JetStream to deliver only messages at or after that stream-global sequence that
// also match the partition's subject filter.
//
// Note on sequence semantics (Pitfall 4): JetStream sequences are stream-global,
// not partition-local. LogEntry.Seq contains the stream-global sequence. Callers
// must treat seq as an opaque cursor — the router and backfill engine do this already.
func (n *NatsEventLog) ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]LogEntry, error) {
	startSeq := fromSeq
	if startSeq == 0 {
		startSeq = 1 // JetStream sequences start at 1; fromSeq=0 is treated as "from start"
	}

	cons, err := n.js.OrderedConsumer(ctx, natsStreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{natsSubject(partition)},
		DeliverPolicy:  jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:    startSeq,
	})
	if err != nil {
		return nil, fmt.Errorf("nats eventlog: ordered consumer for partition %d: %w", partition, err)
	}

	msgs, err := cons.Fetch(limit, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		return nil, fmt.Errorf("nats eventlog: fetch partition %d: %w", partition, err)
	}

	var entries []LogEntry
	for msg := range msgs.Messages() {
		var ev event.ChangeEvent
		if err := json.Unmarshal(msg.Data(), &ev); err != nil {
			return nil, fmt.Errorf("nats eventlog: unmarshal event in partition %d: %w", partition, err)
		}
		meta, err := msg.Metadata()
		if err != nil {
			return nil, fmt.Errorf("nats eventlog: message metadata in partition %d: %w", partition, err)
		}
		entries = append(entries, LogEntry{
			Seq:         meta.Sequence.Stream,
			PartitionID: partition,
			Event:       &ev,
		})
	}

	return entries, msgs.Error()
}

// Close shuts down the NATS connection and the embedded server.
// It is safe to call Close multiple times; subsequent calls are no-ops.
func (n *NatsEventLog) Close() error {
	if n.nc != nil {
		n.nc.Close()
	}
	if n.ns != nil {
		n.ns.Shutdown()
	}
	return nil
}

// Conn returns the underlying *nats.Conn for reuse by cluster components.
// The connection is owned by NatsEventLog and must not be closed by the caller.
func (n *NatsEventLog) Conn() *nats.Conn {
	return n.nc
}

// Ping checks that the JetStream stream is available.
// It fetches the stream info with a 1-second timeout. Returns nil if healthy,
// error otherwise. This matches the BadgerEventLog.Ping() signature used by /healthz.
func (n *NatsEventLog) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err := n.js.Stream(ctx, natsStreamName)
	if err != nil {
		return fmt.Errorf("nats eventlog: ping: %w", err)
	}
	return nil
}
