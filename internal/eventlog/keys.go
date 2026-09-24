package eventlog

import (
	"crypto/sha256"
	"encoding/binary"
)

// Key format constants.
//
// All numeric key components are fixed-width big-endian binary — NEVER decimal ASCII.
// Decimal ASCII would break lexicographic sort order: "s:10" sorts before "s:9".
//
// Partition entry key (14 bytes total):
//
//	[0x50 'P'][partition: 4 bytes BE uint32][0x53 'S'][seq: 8 bytes BE uint64]
//
// Dedup entry key. Keys that fit in Badger are stored raw:
//
//	[0x44 'D'][idempotency_key: variable bytes]
//
// Idempotency keys whose raw form would exceed Badger's 65,000-byte key
// limit are stored as a fixed-size hash instead (issue #80):
//
//	[0x48 'H'][sha256(idempotency_key): 32 bytes]
//
// Partition entry dedup value (12 bytes):
//
//	[partition: 4 bytes BE uint32][seq: 8 bytes BE uint64]
const (
	prefixPart      = 0x50 // 'P' — partition entry prefix
	sepSeq          = 0x53 // 'S' — separator before sequence
	prefixDedup     = 0x44 // 'D' — raw dedup entry prefix
	prefixDedupHash = 0x48 // 'H' — hashed dedup entry prefix

	// maxBadgerKeyLen is Badger's hard maximum key size. Keys longer than
	// this are rejected by Txn.Set with "exceeded 65000 limit".
	maxBadgerKeyLen = 65000
)

// encodePartKey encodes a 14-byte sort-correct key for a partition entry.
// Layout: [0x50][partition 4B BE][0x53][seq 8B BE]
func encodePartKey(partition uint32, seq uint64) []byte {
	key := make([]byte, 14)
	key[0] = prefixPart
	binary.BigEndian.PutUint32(key[1:5], partition)
	key[5] = sepSeq
	binary.BigEndian.PutUint64(key[6:14], seq)
	return key
}

// encodePartPrefix encodes a 5-byte prefix for use with Badger's ValidForPrefix.
// All partition entry keys for the given partition share this prefix.
// Layout: [0x50][partition 4B BE]
func encodePartPrefix(partition uint32) []byte {
	key := make([]byte, 5)
	key[0] = prefixPart
	binary.BigEndian.PutUint32(key[1:5], partition)
	return key
}

// encodeDedupKey encodes the raw dedup index key for an event.
// Layout: [0x44][idempotencyKey bytes]
func encodeDedupKey(idempotencyKey string) []byte {
	b := make([]byte, 1+len(idempotencyKey))
	b[0] = prefixDedup
	copy(b[1:], idempotencyKey)
	return b
}

// dedupIndexKey returns the Badger key for an idempotency key.
//
// A complete key is one prefix byte plus the idempotency key. When that fits
// in maxBadgerKeyLen it stays in the historical raw layout so existing dedup
// entries still match after upgrade. Larger keys, which Badger would reject,
// are stored under a distinct prefix as SHA-256(idempotencyKey).
// The ChangeEvent keeps the original idempotency key; only the index entry is
// hashed. Those oversized keys were never durable before, so there is no
// legacy entry to consult.
func dedupIndexKey(idempotencyKey string) []byte {
	// Badger's limit applies to the complete key: one prefix byte plus the
	// idempotency key. Check the length first so an oversized key is not
	// copied into a temporary raw key that would be discarded.
	if len(idempotencyKey) < maxBadgerKeyLen {
		return encodeDedupKey(idempotencyKey)
	}
	sum := sha256.Sum256([]byte(idempotencyKey))
	b := make([]byte, 1+len(sum))
	b[0] = prefixDedupHash
	copy(b[1:], sum[:])
	return b
}

// encodePartSeq encodes the dedup entry value: partition + seq as 12 bytes.
// This value is stored under the dedup key so future lookups can find the
// exact partition entry if needed.
// Layout: [partition 4B BE][seq 8B BE]
func encodePartSeq(partition uint32, seq uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[0:4], partition)
	binary.BigEndian.PutUint64(b[4:12], seq)
	return b
}

// decodePartKey extracts partition and sequence from a 14-byte partition entry key.
// Inverse of encodePartKey. Panics if key is not 14 bytes.
func decodePartKey(key []byte) (partition uint32, seq uint64) {
	// key[0] = 0x50, key[1:5] = partition, key[5] = 0x53, key[6:14] = seq
	partition = binary.BigEndian.Uint32(key[1:5])
	seq = binary.BigEndian.Uint64(key[6:14])
	return
}
