package eventlog

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/event"
)

type stubPubAckFuture struct {
	ok  chan *jetstream.PubAck
	err chan error
}

func (s stubPubAckFuture) Ok() <-chan *jetstream.PubAck { return s.ok }
func (s stubPubAckFuture) Err() <-chan error            { return s.err }
func (s stubPubAckFuture) Msg() *nats.Msg               { return nil }

func TestCollectBatchPubAcks_NotifiesAckedEventsBeforeFirstError(t *testing.T) {
	evs := []*event.ChangeEvent{
		{IdempotencyKey: "a"},
		{IdempotencyKey: "b"},
		{IdempotencyKey: "c"},
	}
	prepared := []natsPreparedMsg{
		{idx: 0, partition: 1},
		{idx: 1, partition: 1},
		{idx: 2, partition: 2},
	}

	ok0 := make(chan *jetstream.PubAck, 1)
	ok0 <- &jetstream.PubAck{Sequence: 10}
	err1 := make(chan error, 1)
	err1 <- errors.New("puback failed")
	ok2 := make(chan *jetstream.PubAck, 1)
	ok2 <- &jetstream.PubAck{Sequence: 12}

	futures := []jetstream.PubAckFuture{
		stubPubAckFuture{ok: ok0},
		stubPubAckFuture{err: err1},
		stubPubAckFuture{ok: ok2},
	}

	seqs, writtenEvs, writtenSeqs, notifyPartitions, err := collectBatchPubAcks(
		context.Background(), evs, prepared, futures,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pubAck[1]")
	assert.Equal(t, []uint64{10, 0, 12}, seqs)
	require.Len(t, writtenEvs, 2)
	assert.Equal(t, "a", writtenEvs[0].IdempotencyKey)
	assert.Equal(t, "c", writtenEvs[1].IdempotencyKey)
	assert.Equal(t, []uint64{10, 12}, writtenSeqs)
	assert.Equal(t, map[uint32]struct{}{1: {}, 2: {}}, notifyPartitions)
}

func TestCollectBatchPubAcks_SkipsDuplicates(t *testing.T) {
	evs := []*event.ChangeEvent{{IdempotencyKey: "dup"}}
	prepared := []natsPreparedMsg{{idx: 0, partition: 0}}
	ok := make(chan *jetstream.PubAck, 1)
	ok <- &jetstream.PubAck{Sequence: 99, Duplicate: true}
	seqs, writtenEvs, writtenSeqs, notifyPartitions, err := collectBatchPubAcks(
		context.Background(), evs, prepared, []jetstream.PubAckFuture{stubPubAckFuture{ok: ok}},
	)
	require.NoError(t, err)
	assert.Equal(t, []uint64{0}, seqs)
	assert.Empty(t, writtenEvs)
	assert.Empty(t, writtenSeqs)
	assert.Empty(t, notifyPartitions)
}
