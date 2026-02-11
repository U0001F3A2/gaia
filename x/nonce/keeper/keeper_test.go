package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

type KeeperTestSuite struct {
	suite.Suite

	ctx    sdk.Context
	keeper *keeper.Keeper
	addrs  []sdk.AccAddress
}

func (s *KeeperTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))
	s.ctx = testCtx.Ctx.WithBlockTime(time.Unix(1738780800, 0)) // 2025-02-05 16:00:00 UTC

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	storeService := runtime.NewKVStoreService(key)

	k := keeper.NewKeeper(cdc, storeService, "cosmos1authority")
	s.NoError(k.SetParams(s.ctx, types.DefaultParams()))
	s.keeper = k

	// Generate valid test addresses
	s.addrs = make([]sdk.AccAddress, 5)
	for i := 0; i < 5; i++ {
		pk := secp256k1.GenPrivKey().PubKey()
		s.addrs[i] = sdk.AccAddress(pk.Address())
	}
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}

func (s *KeeperTestSuite) TestParamsRoundTrip() {
	params, err := s.keeper.GetParams(s.ctx)
	s.NoError(err)
	s.Equal(types.DefaultParams(), params)

	custom := types.Params{
		PastWindowUs:         uint64(1 * time.Minute.Microseconds()),
		FutureWindowUs:       uint64(2 * time.Minute.Microseconds()),
		TimestampNonceCutoff: 1 << 32,
	}
	s.NoError(s.keeper.SetParams(s.ctx, custom))

	got, err := s.keeper.GetParams(s.ctx)
	s.NoError(err)
	s.Equal(custom, got)
}

func (s *KeeperTestSuite) TestValidateAndConsumeTimestampNonce() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Success: nonce at block time
	err := s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, blockTimeUs)
	s.NoError(err)

	// Duplicate: same nonce should fail
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, blockTimeUs)
	s.ErrorIs(err, types.ErrNonceDuplicate)

	// Same timestamp, different address: should succeed
	addr2 := s.addrs[1]
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr2, blockTimeUs)
	s.NoError(err)

	// Expired: nonce too far in the past
	expiredNonce := blockTimeUs - types.DefaultPastWindowUs - 1
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, expiredNonce)
	s.ErrorIs(err, types.ErrNonceExpired)

	// Future: nonce too far in the future
	futureNonce := blockTimeUs + types.DefaultFutureWindowUs + 1
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, futureNonce)
	s.ErrorIs(err, types.ErrNonceTooFarInFuture)

	// Edge: nonce at exact lower bound (should succeed)
	lowerBound := blockTimeUs - types.DefaultPastWindowUs
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, lowerBound)
	s.NoError(err)

	// Edge: nonce at exact upper bound (should succeed)
	upperBound := blockTimeUs + types.DefaultFutureWindowUs
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, upperBound)
	s.NoError(err)
}

func (s *KeeperTestSuite) TestPruneExpiredNonces() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Set up nonces:
	// 1. Definite old (expired)
	oldNonce := blockTimeUs - types.DefaultPastWindowUs - uint64(1*time.Second.Microseconds())
	// 2. Exact boundary (should be kept because `nonce < cutoff` usually defines expiration, exact match is strictly valid)
	//    Wait, check logic: usually `cutoff = now - window`. If `nonce < cutoff`, it is expired.
	//    If `nonce == cutoff`, it is valid.
	boundaryNonce := blockTimeUs - types.DefaultPastWindowUs
	// 3. Recent (valid)
	recentNonce := blockTimeUs - uint64(1*time.Minute.Microseconds())
	// 4. Current (valid)
	currentNonce := blockTimeUs

	// Manually set nonces (bypassing validation to set expired ones)
	s.NoError(s.keeper.SetNonce(s.ctx, addr, oldNonce))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, boundaryNonce))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, recentNonce))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, currentNonce))

	// Verify all exist
	has, _ := s.keeper.HasNonce(s.ctx, addr, oldNonce)
	s.True(has)
	has, _ = s.keeper.HasNonce(s.ctx, addr, boundaryNonce)
	s.True(has)

	// Prune
	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))

	// Old nonce should be pruned
	has, _ = s.keeper.HasNonce(s.ctx, addr, oldNonce)
	s.False(has, "old nonce should be pruned")

	// Boundary, Recent and current should remain
	has, _ = s.keeper.HasNonce(s.ctx, addr, boundaryNonce)
	s.True(has, "boundary nonce should remain")
	has, _ = s.keeper.HasNonce(s.ctx, addr, recentNonce)
	s.True(has, "recent nonce should remain")
	has, _ = s.keeper.HasNonce(s.ctx, addr, currentNonce)
	s.True(has, "current nonce should remain")
}

func (s *KeeperTestSuite) TestGenesisInitExport() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Set some nonces
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs+1000))

	// Export
	gs := s.keeper.ExportGenesis(s.ctx)
	s.Equal(types.DefaultParams(), gs.Params)
	s.Len(gs.NonceEntries, 2)

	// Init fresh keeper with exported state
	// We need a fresh context and keeper but can reuse setup logic structure
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test_2"))
	ctx2 := testCtx.Ctx.WithBlockTime(time.Unix(1738780800, 0))

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	storeService := runtime.NewKVStoreService(key)
	k2 := keeper.NewKeeper(cdc, storeService, "cosmos1authority")

	k2.InitGenesis(ctx2, gs)

	// Verify nonces exist in new keeper
	has, err := k2.HasNonce(ctx2, addr, blockTimeUs)
	s.NoError(err)
	s.True(has)

	has, err = k2.HasNonce(ctx2, addr, blockTimeUs+1000)
	s.NoError(err)
	s.True(has)
}

func (s *KeeperTestSuite) TestHasNonceQuery() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Before consumption
	has, err := s.keeper.HasNonce(s.ctx, addr, blockTimeUs)
	s.NoError(err)
	s.False(has)

	// After consumption
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))
	has, err = s.keeper.HasNonce(s.ctx, addr, blockTimeUs)
	s.NoError(err)
	s.True(has)
}
