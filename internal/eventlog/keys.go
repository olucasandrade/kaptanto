package eventlog

import "encoding/binary"

// Key format constants.
//
// All numeric key components are fixed-width big-endian binary — NEVER decimal ASCII.
// Decimal ASCII would break lexicographic sort order: "s:10" sorts before "s:9".
//
// Partition entry key (14 bytes total):
//
//	[0x50 'P'][partition: 4 bytes BE uint32][0x53 'S'][seq: 8 bytes BE uint64]
//
// Dedup entry key (variable length):
//
//	[0x44 'D'][idempotency_key: variable bytes]
//
// Partition entry dedup value (12 bytes):
//
//	[partition: 4 bytes BE uint32][seq: 8 bytes BE uint64]
const (
	prefixPart  = 0x50 // 'P' — partition entry prefix
	sepSeq      = 0x53 // 'S' — separator before sequence
	prefixDedup = 0x44 // 'D' — dedup entry prefix
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

// encodeDedupKey encodes the dedup index key for an event.
// Layout: [0x44][idempotencyKey bytes]
func encodeDedupKey(idempotencyKey string) []byte {
	b := make([]byte, 1+len(idempotencyKey))
	b[0] = prefixDedup
	copy(b[1:], idempotencyKey)
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
