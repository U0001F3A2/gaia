package types

import (
	"encoding/binary"
)

const (
	ModuleName = "nonce"
	StoreKey   = ModuleName
)

var (
	// ParamsKey is the key for the module params.
	ParamsKey = []byte{0x01}

	// NonceKeyPrefix is the prefix for timestamp nonce entries in the KV store.
	// Key format: NonceKeyPrefix | timestamp_us (8 bytes big-endian) | address (variable)
	// Timestamp-first ordering enables efficient range deletion during pruning.
	NonceKeyPrefix = []byte{0x02}

	// PruneWatermarkKey stores the highest pruning cutoff (microseconds) ever used.
	// Monotonically increasing. Prevents replay attacks when past_window_us expands
	// via governance: nonces below the watermark are always rejected, even if the
	// expanded window would otherwise re-admit them.
	PruneWatermarkKey = []byte{0x03}

	// NonceKeyMinLen is the minimum valid nonce key length: prefix + 8-byte timestamp.
	// Keys shorter than this cannot contain a valid timestamp and should be skipped.
	NonceKeyMinLen = len(NonceKeyPrefix) + 8
)

// BuildNonceKey constructs a KV store key for a specific (timestamp, address) pair.
func BuildNonceKey(timestampUs uint64, addr []byte) []byte {
	pfx := len(NonceKeyPrefix)
	key := make([]byte, pfx+8+len(addr))
	copy(key, NonceKeyPrefix)
	binary.BigEndian.PutUint64(key[pfx:pfx+8], timestampUs)
	copy(key[pfx+8:], addr)
	return key
}

// BuildNoncePrefixUpTo returns a key that serves as the exclusive upper bound
// for iterating nonces with timestamp < cutoffUs.
func BuildNoncePrefixUpTo(cutoffUs uint64) []byte {
	pfx := len(NonceKeyPrefix)
	key := make([]byte, pfx+8)
	copy(key, NonceKeyPrefix)
	binary.BigEndian.PutUint64(key[pfx:pfx+8], cutoffUs)
	return key
}

// NonceIteratorPrefix returns the prefix for iterating all nonce entries.
func NonceIteratorPrefix() []byte {
	return NonceKeyPrefix
}

// ParseNonceKey extracts (timestampUs, addr) from a full nonce key.
// Returns zero values if key is malformed (< NonceKeyMinLen bytes).
func ParseNonceKey(key []byte) (timestampUs uint64, addr []byte) {
	if len(key) < NonceKeyMinLen {
		return 0, nil
	}
	pfx := len(NonceKeyPrefix)
	timestampUs = binary.BigEndian.Uint64(key[pfx : pfx+8])
	addr = key[pfx+8:]
	return
}
