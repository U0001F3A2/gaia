package ante_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/testutil"

	"github.com/cosmos/gaia/v26/x/nonce/ante"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func TestTimestampSignerContext(t *testing.T) {
	key := storetypes.NewKVStoreKey("test")
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient"))
	ctx := testCtx.Ctx

	// Initially nil
	signers := ante.GetTimestampSigners(ctx)
	require.Nil(t, signers)

	// Set and retrieve
	set := ante.TimestampSignerSet{"addr1": true, "addr2": true}
	ctx = ante.WithTimestampSigners(ctx, set)
	got := ante.GetTimestampSigners(ctx)
	require.NotNil(t, got)
	require.True(t, got["addr1"])
	require.True(t, got["addr2"])
	require.False(t, got["addr3"])
}

func TestNonceCutoffThreshold(t *testing.T) {
	// Verify that the cutoff is 2^40
	require.Equal(t, uint64(1<<40), types.TimestampNonceCutoff)

	// A typical account sequence (e.g., 42) should be below the cutoff
	require.True(t, uint64(42) < types.TimestampNonceCutoff)

	// A microsecond timestamp (e.g., 2025-02-05) should be above the cutoff
	ts := uint64(time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC).UnixMicro())
	require.True(t, ts >= types.TimestampNonceCutoff)
}
