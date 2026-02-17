package types_test

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func TestParseNonceKey_Malformed(t *testing.T) {
	tests := []struct {
		name    string
		key     []byte
		wantTs  uint64
		wantNil bool // true if addr should be nil
	}{
		{
			name:    "nil key",
			key:     nil,
			wantTs:  0,
			wantNil: true,
		},
		{
			name:    "empty key",
			key:     []byte{},
			wantTs:  0,
			wantNil: true,
		},
		{
			name:    "only prefix byte",
			key:     []byte{0x02},
			wantTs:  0,
			wantNil: true,
		},
		{
			name:    "prefix plus partial timestamp (5 bytes)",
			key:     []byte{0x02, 0x00, 0x00, 0x00, 0x01},
			wantTs:  0,
			wantNil: true,
		},
		{
			name:    "8 bytes (one short of valid)",
			key:     []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			wantTs:  0,
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, addr := types.ParseNonceKey(tc.key)
			require.Equal(t, tc.wantTs, ts)
			if tc.wantNil {
				require.Nil(t, addr)
			}
		})
	}
}

func TestParseNonceKey_MinimumValidKey(t *testing.T) {
	// Exactly 9 bytes: prefix + 8-byte timestamp, no address bytes.
	// This is valid but yields an empty (not nil) address slice.
	key := make([]byte, 9)
	key[0] = types.NonceKeyPrefix[0]
	binary.BigEndian.PutUint64(key[1:9], 42)

	ts, addr := types.ParseNonceKey(key)
	require.Equal(t, uint64(42), ts)
	require.NotNil(t, addr, "addr should be empty slice, not nil")
	require.Empty(t, addr)
}

func TestParseNonceKey_RoundTrip(t *testing.T) {
	addr := []byte("cosmos1testaddr")
	original := uint64(1738780800000000)

	key := types.BuildNonceKey(original, addr)
	gotTs, gotAddr := types.ParseNonceKey(key)
	require.Equal(t, original, gotTs)
	require.Equal(t, addr, gotAddr)
}

func TestBuildNoncePrefixUpTo(t *testing.T) {
	cutoff := uint64(999)
	key := types.BuildNoncePrefixUpTo(cutoff)
	require.Len(t, key, 9)
	require.Equal(t, types.NonceKeyPrefix[0], key[0])
	require.Equal(t, cutoff, binary.BigEndian.Uint64(key[1:9]))
}
