package keeper_test

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

	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func setupKeeper(t *testing.T) (*keeper.Keeper, sdk.Context) {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1738780800, 0)) // 2025-02-05 16:00:00 UTC

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	storeService := runtime.NewKVStoreService(key)

	k := keeper.NewKeeper(cdc, storeService, "cosmos1authority")
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	return k, ctx
}

func TestParamsRoundTrip(t *testing.T) {
	k, ctx := setupKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultParams(), params)

	custom := types.Params{
		PastWindowUs:         60_000_000,
		FutureWindowUs:       120_000_000,
		TimestampNonceCutoff: 1 << 32,
	}
	require.NoError(t, k.SetParams(ctx, custom))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, custom, got)
}

func TestValidateAndConsumeTimestampNonce(t *testing.T) {
	k, ctx := setupKeeper(t)

	blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
	addr := []byte("cosmos1testaddr1234567890")

	// Success: nonce at block time
	err := k.ValidateAndConsumeTimestampNonce(ctx, addr, blockTimeUs)
	require.NoError(t, err)

	// Duplicate: same nonce should fail
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr, blockTimeUs)
	require.ErrorIs(t, err, types.ErrNonceDuplicate)

	// Same timestamp, different address: should succeed
	addr2 := []byte("cosmos1testaddr9876543210")
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr2, blockTimeUs)
	require.NoError(t, err)

	// Expired: nonce too far in the past
	expiredNonce := blockTimeUs - types.DefaultPastWindowUs - 1
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr, expiredNonce)
	require.ErrorIs(t, err, types.ErrNonceExpired)

	// Future: nonce too far in the future
	futureNonce := blockTimeUs + types.DefaultFutureWindowUs + 1
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr, futureNonce)
	require.ErrorIs(t, err, types.ErrNonceTooFarInFuture)

	// Edge: nonce at exact lower bound (should succeed)
	lowerBound := blockTimeUs - types.DefaultPastWindowUs
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr, lowerBound)
	require.NoError(t, err)

	// Edge: nonce at exact upper bound (should succeed)
	upperBound := blockTimeUs + types.DefaultFutureWindowUs
	err = k.ValidateAndConsumeTimestampNonce(ctx, addr, upperBound)
	require.NoError(t, err)
}

func TestPruneExpiredNonces(t *testing.T) {
	k, ctx := setupKeeper(t)

	blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
	addr := []byte("cosmos1testaddr1234567890")

	// Set up nonces: one old, one recent, one at block time
	oldNonce := blockTimeUs - types.DefaultPastWindowUs - 1_000_000 // 1 sec older than window
	recentNonce := blockTimeUs - 60_000_000                         // 1 min ago (within window)
	currentNonce := blockTimeUs

	// Manually set nonces (bypassing validation to set expired ones)
	require.NoError(t, k.SetNonce(ctx, addr, oldNonce))
	require.NoError(t, k.SetNonce(ctx, addr, recentNonce))
	require.NoError(t, k.SetNonce(ctx, addr, currentNonce))

	// Verify all exist
	has, _ := k.HasNonce(ctx, addr, oldNonce)
	require.True(t, has)
	has, _ = k.HasNonce(ctx, addr, recentNonce)
	require.True(t, has)
	has, _ = k.HasNonce(ctx, addr, currentNonce)
	require.True(t, has)

	// Prune
	require.NoError(t, k.PruneExpiredNonces(ctx))

	// Old nonce should be pruned
	has, _ = k.HasNonce(ctx, addr, oldNonce)
	require.False(t, has)

	// Recent and current should remain
	has, _ = k.HasNonce(ctx, addr, recentNonce)
	require.True(t, has)
	has, _ = k.HasNonce(ctx, addr, currentNonce)
	require.True(t, has)
}

func TestGenesisInitExport(t *testing.T) {
	k, ctx := setupKeeper(t)

	blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
	addr := sdk.AccAddress([]byte("cosmos1testaddr1234567890"))

	// Set some nonces
	require.NoError(t, k.SetNonce(ctx, addr, blockTimeUs))
	require.NoError(t, k.SetNonce(ctx, addr, blockTimeUs+1000))

	// Export
	gs := k.ExportGenesis(ctx)
	require.Equal(t, types.DefaultParams(), gs.Params)
	require.Len(t, gs.NonceEntries, 2)

	// Init fresh keeper with exported state
	k2, ctx2 := setupKeeper(t)
	k2.InitGenesis(ctx2, gs)

	// Verify nonces exist in new keeper
	has, err := k2.HasNonce(ctx2, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has)

	has, err = k2.HasNonce(ctx2, addr, blockTimeUs+1000)
	require.NoError(t, err)
	require.True(t, has)
}

func TestHasNonceQuery(t *testing.T) {
	k, ctx := setupKeeper(t)

	blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
	addr := []byte("cosmos1testaddr1234567890")

	// Before consumption
	has, err := k.HasNonce(ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.False(t, has)

	// After consumption
	require.NoError(t, k.SetNonce(ctx, addr, blockTimeUs))
	has, err = k.HasNonce(ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has)
}
