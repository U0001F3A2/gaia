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
)

// BuildNonceKey constructs a KV store key for a specific (timestamp, address) pair.
func BuildNonceKey(timestampUs uint64, addr []byte) []byte {
	key := make([]byte, 1+8+len(addr))
	key[0] = NonceKeyPrefix[0]
	binary.BigEndian.PutUint64(key[1:9], timestampUs)
	copy(key[9:], addr)
	return key
}

// BuildNoncePrefixUpTo returns a key that serves as the exclusive upper bound
// for iterating nonces with timestamp < cutoffUs.
func BuildNoncePrefixUpTo(cutoffUs uint64) []byte {
	key := make([]byte, 1+8)
	key[0] = NonceKeyPrefix[0]
	binary.BigEndian.PutUint64(key[1:9], cutoffUs)
	return key
}

// NonceIteratorPrefix returns the prefix for iterating all nonce entries.
func NonceIteratorPrefix() []byte {
	return NonceKeyPrefix
}

// ParseNonceKey extracts (timestampUs, addr) from a full nonce key.
func ParseNonceKey(key []byte) (timestampUs uint64, addr []byte) {
	timestampUs = binary.BigEndian.Uint64(key[1:9])
	addr = key[9:]
	return
}
