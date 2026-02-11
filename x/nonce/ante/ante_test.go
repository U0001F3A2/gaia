package ante_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/ante"
	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func setupNonceKeeper(t *testing.T) (*keeper.Keeper, sdk.Context) {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1738780800, 0))

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	storeService := runtime.NewKVStoreService(key)

	k := keeper.NewKeeper(cdc, storeService, "cosmos1authority")
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	return k, ctx
}

func TestNonceKeeperAdapter(t *testing.T) {
	k, ctx := setupNonceKeeper(t)
	adapter := ante.NonceKeeperAdapter{K: k}

	// Test GetParams
	params, err := adapter.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultParams(), params)

	// Test ValidateAndConsumeTimestampNonce
	blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
	addr := []byte("cosmos1testaddr1234567890")
	err = adapter.ValidateAndConsumeTimestampNonce(ctx, addr, blockTimeUs)
	require.NoError(t, err)

	// Duplicate should fail
	err = adapter.ValidateAndConsumeTimestampNonce(ctx, addr, blockTimeUs)
	require.ErrorIs(t, err, types.ErrNonceDuplicate)
}

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
	require.Equal(t, uint64(1<<40), types.DefaultTimestampNonceCutoff)

	// A typical account sequence (e.g., 42) should be below the cutoff
	require.True(t, uint64(42) < types.DefaultTimestampNonceCutoff)

	// A microsecond timestamp (e.g., 2025-02-05) should be above the cutoff
	ts := uint64(time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC).UnixMicro())
	require.True(t, ts >= types.DefaultTimestampNonceCutoff)
}
