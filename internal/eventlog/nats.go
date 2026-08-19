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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// Compile-time assertion: NatsEventLog must implement EventLog.
var _ EventLog = (*NatsEventLog)(nil)

// Compile-time assertion: NatsEventLog implements PartitionNotifier.
var _ PartitionNotifier = (*NatsEventLog)(nil)

var _ PartitionReleaser = (*NatsEventLog)(nil)

// Compile-time assertion: NatsEventLog implements AppendObservable so
// WatermarkChecker can build the O(1) index in cluster mode (BKF-02).
var _ AppendObservable = (*NatsEventLog)(nil)

const (
	// natsStreamName is the single JetStream stream that holds all 64 partition subjects.
	// One stream per kaptanto instance — one Raft group, one SyncAlways applies.
	natsStreamName = "kaptanto-events"

	// natsSubjectPattern matches all partition subjects for use in StreamConfig.Subjects.
	natsSubjectPattern = "kaptanto.events.*"

	natsPullInactiveThreshold = 30 * time.Second
	natsConsumerDeleteTimeout = 2 * time.Second
)

// natsSubject returns the JetStream subject for the given partition number.
// Subject format: "kaptanto.events.{partition:05d}" — zero-padded for lexicographic ordering.
func natsSubject(partition uint32) string {
	return fmt.Sprintf("kaptanto.events.%05d", partition)
}

func newNatsInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
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

// natsPartitionReader holds the long-lived JetStream pull consumer for one
// partition. nextSeq is the fromSeq this consumer will next deliver; a
// mismatch (router rewind, watermark scan) deletes and recreates it.
type natsPartitionReader struct {
	mu      sync.Mutex
	cons    jetstream.Consumer
	nextSeq uint64
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
	notifyChs     []chan struct{} // one depth-1 buffered channel per partition
	instanceID    string
	readers       []*natsPartitionReader

	obsMu     sync.RWMutex
	observers map[int]AppendObserver // keyed by monotonically increasing id for stable unregister
	nextObsID int
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

	notifyChs := make([]chan struct{}, numPartitions)
	readers := make([]*natsPartitionReader, numPartitions)
	for i := uint32(0); i < numPartitions; i++ {
		notifyChs[i] = make(chan struct{}, 1)
		readers[i] = &natsPartitionReader{}
	}

	return &NatsEventLog{
		ns:            ns,
		nc:            nc,
		js:            js,
		stream:        stream,
		numPartitions: numPartitions,
		notifyChs:     notifyChs,
		instanceID:    newNatsInstanceID(),
		readers:       readers,
		observers:     make(map[int]AppendObserver),
	}, nil
}

// RegisterObserver implements AppendObservable. obs is called synchronously,
// after a successful (non-duplicate) PubAck, on every future Append/AppendBatch
// that writes at least one non-duplicate event. An in-flight Append that has
// not yet copied the observer snapshot will still notify obs; a registration
// that races the snapshot may miss that one append (the next one is seen).
func (n *NatsEventLog) RegisterObserver(obs AppendObserver) (unregister func()) {
	n.obsMu.Lock()
	id := n.nextObsID
	n.nextObsID++
	n.observers[id] = obs
	n.obsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			n.obsMu.Lock()
			delete(n.observers, id)
			n.obsMu.Unlock()
		})
	}
}

// notifyObservers dispatches evs/seqs (already filtered to non-duplicates by
// the caller) to every registered observer, synchronously and in the calling
// goroutine — this is what makes RegisterObserver's "sees every future
// commit" guarantee hold: PubAck has already confirmed the JetStream write
// before this runs, and this runs before Append/AppendBatch returns.
//
// Observers are copied under obsMu and callbacks run after the lock is
// released. ObserveAppend may call the observer's unregister func (which
// takes obsMu.Lock) without deadlocking. A snapshot observer still receives
// this dispatch even if it unregisters during the callback; later appends
// will not see it.
func (n *NatsEventLog) notifyObservers(evs []*event.ChangeEvent, seqs []uint64) {
	if len(evs) == 0 {
		return
	}
	n.obsMu.RLock()
	observers := make([]AppendObserver, 0, len(n.observers))
	for _, obs := range n.observers {
		observers = append(observers, obs)
	}
	n.obsMu.RUnlock()
	for _, obs := range observers {
		obs.ObserveAppend(evs, seqs)
	}
}

// NotifyCh returns the read-only notify channel for the given partition.
func (n *NatsEventLog) NotifyCh(partition uint32) <-chan struct{} {
	return n.notifyChs[partition]
}

func (n *NatsEventLog) notify(partition uint32) {
	select {
	case n.notifyChs[partition] <- struct{}{}:
	default:
	}
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
	// Observers must not fire for duplicates, and must not fire before PubAck.
	if ack.Duplicate {
		return 0, nil
	}

	n.notify(partition)
	n.notifyObservers([]*event.ChangeEvent{ev}, []uint64{ack.Sequence})
	return ack.Sequence, nil
}

// AppendBatch durably writes all events using pipelined PublishMsgAsync calls,
// collecting PubAck futures before returning. CHK-01 holds because callers
// must not advance the source checkpoint until AppendBatch returns without error.
func (n *NatsEventLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	if len(evs) == 0 {
		return nil, nil
	}

	preparedMsgs := make([]natsPreparedMsg, 0, len(evs))
	for i, ev := range evs {
		partition := PartitionOf(ev.Key, n.numPartitions)
		data, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("nats eventlog: marshal event[%d]: %w", i, err)
		}
		msg := &nats.Msg{
			Subject: natsSubject(partition),
			Data:    data,
			Header:  nats.Header{nats.MsgIdHdr: []string{ev.IdempotencyKey}},
		}
		preparedMsgs = append(preparedMsgs, natsPreparedMsg{msg: msg, partition: partition, idx: i})
	}

	futures := make([]jetstream.PubAckFuture, 0, len(preparedMsgs))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, pm := range preparedMsgs {
		fut, err := n.js.PublishMsgAsync(pm.msg)
		if err != nil {
			return nil, fmt.Errorf("nats eventlog: AppendBatch async publish[%d]: %w", pm.idx, err)
		}
		futures = append(futures, fut)
	}

	seqs, writtenEvs, writtenSeqs, notifyPartitions, firstErr := collectBatchPubAcks(ctx, evs, preparedMsgs, futures)

	for partition := range notifyPartitions {
		n.notify(partition)
	}
	// Notify observers for every acknowledged non-duplicate event even when a
	// later PubAck fails. Those writes are durable; a retry will hit JetStream
	// dedup (seq==0) and would otherwise leave WatermarkChecker with a stale index.
	n.notifyObservers(writtenEvs, writtenSeqs)
	if firstErr != nil {
		return seqs, firstErr
	}
	return seqs, nil
}

// natsPreparedMsg is one AppendBatch publish waiting on its PubAck future.
type natsPreparedMsg struct {
	msg       *nats.Msg
	partition uint32
	idx       int
}

// collectBatchPubAcks drains every PubAck future. The first error is preserved
// but the rest of the batch is still collected so already-acked events can be
// observed before the error is returned.
func collectBatchPubAcks(ctx context.Context, evs []*event.ChangeEvent, preparedMsgs []natsPreparedMsg, futures []jetstream.PubAckFuture) (
	seqs []uint64,
	writtenEvs []*event.ChangeEvent,
	writtenSeqs []uint64,
	notifyPartitions map[uint32]struct{},
	err error,
) {
	seqs = make([]uint64, len(evs))
	writtenEvs = make([]*event.ChangeEvent, 0, len(evs))
	writtenSeqs = make([]uint64, 0, len(evs))
	notifyPartitions = make(map[uint32]struct{})
	var firstErr error
	for j, fut := range futures {
		pm := preparedMsgs[j]
		select {
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = fmt.Errorf("nats eventlog: AppendBatch pubAck[%d]: %w", pm.idx, ctx.Err())
			}
		case ack := <-fut.Ok():
			if ack.Duplicate {
				seqs[pm.idx] = 0
				continue
			}
			seqs[pm.idx] = ack.Sequence
			notifyPartitions[pm.partition] = struct{}{}
			writtenEvs = append(writtenEvs, evs[pm.idx])
			writtenSeqs = append(writtenSeqs, ack.Sequence)
		case ackErr := <-fut.Err():
			if firstErr == nil {
				firstErr = fmt.Errorf("nats eventlog: AppendBatch pubAck[%d]: %w", pm.idx, ackErr)
			}
		}
	}
	return seqs, writtenEvs, writtenSeqs, notifyPartitions, firstErr
}

// ReadPartition returns up to limit events from the given partition, starting at
// fromSeq (inclusive), using a long-lived JetStream pull consumer per partition.
//
// The consumer is created once and reused while fromSeq matches the next
// sequence the consumer will deliver. A rewind (failed delivery) or a jump
// (watermark scan vs router) deletes and recreates it with OptStartSeq=fromSeq.
// Empty probes use FetchNoWait so idle notify can wake the partition without a
// 2s FetchMaxWait tail.
//
// Note on sequence semantics (Pitfall 4): JetStream sequences are stream-global,
// not partition-local. LogEntry.Seq contains the stream-global sequence. Callers
// must treat seq as an opaque cursor — the router and backfill engine do this already.
func (n *NatsEventLog) ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}

	startSeq := fromSeq
	if startSeq == 0 {
		startSeq = 1
	}

	r := n.readers[partition]
	r.mu.Lock()
	defer r.mu.Unlock()

	cons, err := n.ensurePullConsumer(ctx, partition, r, startSeq)
	if err != nil {
		return nil, err
	}

	msgs, err := cons.FetchNoWait(limit)
	if err != nil {
		n.deletePullConsumerLocked(r)
		return nil, fmt.Errorf("nats eventlog: fetch partition %d: %w", partition, err)
	}

	entries, lastSeq, err := collectPullEntries(msgs, partition)
	if err != nil {
		n.deletePullConsumerLocked(r)
		return nil, err
	}
	if lastSeq != 0 {
		r.nextSeq = lastSeq + 1
	}
	return entries, nil
}

func collectPullEntries(msgs jetstream.MessageBatch, partition uint32) ([]LogEntry, uint64, error) {
	var entries []LogEntry
	var lastSeq uint64
	for msg := range msgs.Messages() {
		data := msg.Data()
		raw := make([]byte, len(data))
		copy(raw, data)

		var ev event.ChangeEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, 0, fmt.Errorf("nats eventlog: unmarshal event in partition %d: %w", partition, err)
		}
		meta, err := msg.Metadata()
		if err != nil {
			return nil, 0, fmt.Errorf("nats eventlog: message metadata in partition %d: %w", partition, err)
		}
		lastSeq = meta.Sequence.Stream
		entries = append(entries, LogEntry{
			Seq:         lastSeq,
			PartitionID: partition,
			Event:       &ev,
			Raw:         raw,
		})
	}
	if err := msgs.Error(); err != nil {
		return nil, 0, err
	}
	return entries, lastSeq, nil
}

func (n *NatsEventLog) pullConsumerName(partition uint32) string {
	return fmt.Sprintf("kaptanto-rp-%05d-%s", partition, n.instanceID)
}

func (n *NatsEventLog) ensurePullConsumer(ctx context.Context, partition uint32, r *natsPartitionReader, startSeq uint64) (jetstream.Consumer, error) {
	if r.cons != nil && r.nextSeq == startSeq {
		return r.cons, nil
	}
	n.deletePullConsumerLocked(r)

	name := n.pullConsumerName(partition)
	_ = n.stream.DeleteConsumer(ctx, name)

	cons, err := n.stream.CreateConsumer(ctx, jetstream.ConsumerConfig{
		Name:              name,
		FilterSubject:     natsSubject(partition),
		DeliverPolicy:     jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:       startSeq,
		AckPolicy:         jetstream.AckNonePolicy,
		ReplayPolicy:      jetstream.ReplayInstantPolicy,
		MemoryStorage:     true,
		InactiveThreshold: natsPullInactiveThreshold,
		Replicas:          1,
	})
	if err != nil {
		return nil, fmt.Errorf("nats eventlog: pull consumer for partition %d: %w", partition, err)
	}
	r.cons = cons
	r.nextSeq = startSeq
	return cons, nil
}

func (n *NatsEventLog) deletePullConsumerLocked(r *natsPartitionReader) {
	if r.cons == nil {
		return
	}
	name := r.cons.CachedInfo().Name
	r.cons = nil
	r.nextSeq = 0
	delCtx, cancel := context.WithTimeout(context.Background(), natsConsumerDeleteTimeout)
	defer cancel()
	_ = n.stream.DeleteConsumer(delCtx, name)
}

// ReleasePartition stops and deletes the long-lived pull consumer for partition.
// Safe to call when no consumer exists. Cluster ownership changes and
// runPartition shutdown must call this so consumers do not leak across a steal.
func (n *NatsEventLog) ReleasePartition(partition uint32) {
	if partition >= n.numPartitions {
		return
	}
	r := n.readers[partition]
	r.mu.Lock()
	defer r.mu.Unlock()
	n.deletePullConsumerLocked(r)
}

// Close shuts down the NATS connection and the embedded server.
// It is safe to call Close multiple times; subsequent calls are no-ops.
func (n *NatsEventLog) Close() error {
	for p := uint32(0); p < n.numPartitions; p++ {
		n.ReleasePartition(p)
	}
	if n.nc != nil {
		n.nc.Close()
		n.nc = nil
	}
	if n.ns != nil {
		n.ns.Shutdown()
		n.ns = nil
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
