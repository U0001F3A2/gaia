package keeper_test

import (
	"math"
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
	"github.com/cosmos/cosmos-sdk/types/query"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

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

// --- GRPC Query Tests ---

func (s *KeeperTestSuite) TestGRPCQueryParams() {
	q := keeper.Querier{Keeper: s.keeper}
	resp, err := q.Params(s.ctx, &types.QueryParamsRequest{})
	s.NoError(err)
	s.Equal(types.DefaultParams(), resp.Params)
}

func (s *KeeperTestSuite) TestGRPCQueryHasNonce() {
	q := keeper.Querier{Keeper: s.keeper}
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Not consumed yet
	resp, err := q.HasNonce(s.ctx, &types.QueryHasNonceRequest{
		Address:     addr.String(),
		TimestampUs: blockTimeUs,
	})
	s.NoError(err)
	s.False(resp.HasNonce)

	// Consume and re-query
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))
	resp, err = q.HasNonce(s.ctx, &types.QueryHasNonceRequest{
		Address:     addr.String(),
		TimestampUs: blockTimeUs,
	})
	s.NoError(err)
	s.True(resp.HasNonce)

	// Invalid bech32 address
	_, err = q.HasNonce(s.ctx, &types.QueryHasNonceRequest{
		Address:     "invalid",
		TimestampUs: blockTimeUs,
	})
	s.Error(err)
}

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress() {
	q := keeper.Querier{Keeper: s.keeper}
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr0 := s.addrs[0]
	addr1 := s.addrs[1]

	// Set nonces for addr0
	s.NoError(s.keeper.SetNonce(s.ctx, addr0, blockTimeUs))
	s.NoError(s.keeper.SetNonce(s.ctx, addr0, blockTimeUs+1000))
	s.NoError(s.keeper.SetNonce(s.ctx, addr0, blockTimeUs+2000))

	// Set nonces for addr1 (should not appear in addr0 query)
	s.NoError(s.keeper.SetNonce(s.ctx, addr1, blockTimeUs+500))

	resp, err := q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: addr0.String(),
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 3)
	s.Contains(resp.TimestampNonces, blockTimeUs)
	s.Contains(resp.TimestampNonces, blockTimeUs+1000)
	s.Contains(resp.TimestampNonces, blockTimeUs+2000)

	// addr1 should only have 1
	resp, err = q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: addr1.String(),
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 1)
	s.Equal(blockTimeUs+500, resp.TimestampNonces[0])

	// addr with no nonces
	resp, err = q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: s.addrs[2].String(),
	})
	s.NoError(err)
	s.Empty(resp.TimestampNonces)
}

// --- Key Encoding Tests ---

func (s *KeeperTestSuite) TestKeyEncodingRoundTrip() {
	addr := s.addrs[0]
	ts := uint64(1738780800000000)

	key := types.BuildNonceKey(ts, addr)
	gotTs, gotAddr := types.ParseNonceKey(key)
	s.Equal(ts, gotTs)
	s.Equal([]byte(addr), gotAddr)
}

func (s *KeeperTestSuite) TestKeyOrderingIsTimestampFirst() {
	addr := s.addrs[0]
	key1 := types.BuildNonceKey(100, addr)
	key2 := types.BuildNonceKey(200, addr)
	key3 := types.BuildNonceKey(200, s.addrs[1])

	// key1 < key2 (earlier timestamp sorts first)
	s.True(string(key1) < string(key2))
	// key2 and key3 share timestamp prefix, differ by address
	s.Equal(key2[:9], key3[:9])
}

// --- Prune Edge Cases ---

func (s *KeeperTestSuite) TestPruneMultipleAddresses() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr0 := s.addrs[0]
	addr1 := s.addrs[1]

	expired := blockTimeUs - types.DefaultPastWindowUs - 1_000_000
	valid := blockTimeUs

	s.NoError(s.keeper.SetNonce(s.ctx, addr0, expired))
	s.NoError(s.keeper.SetNonce(s.ctx, addr1, expired))
	s.NoError(s.keeper.SetNonce(s.ctx, addr0, valid))
	s.NoError(s.keeper.SetNonce(s.ctx, addr1, valid))

	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))

	// Expired nonces pruned for both addresses
	has, _ := s.keeper.HasNonce(s.ctx, addr0, expired)
	s.False(has)
	has, _ = s.keeper.HasNonce(s.ctx, addr1, expired)
	s.False(has)

	// Valid nonces remain for both
	has, _ = s.keeper.HasNonce(s.ctx, addr0, valid)
	s.True(has)
	has, _ = s.keeper.HasNonce(s.ctx, addr1, valid)
	s.True(has)
}

func (s *KeeperTestSuite) TestPruneEarlyChainUnderflow() {
	// Simulate early chain: block time < past window
	earlyCtx := s.ctx.WithBlockTime(time.Unix(60, 0)) // 60 seconds after epoch
	s.NoError(s.keeper.SetParams(earlyCtx, types.DefaultParams()))

	addr := s.addrs[0]
	ts := uint64(earlyCtx.BlockTime().UnixMicro())
	s.NoError(s.keeper.SetNonce(earlyCtx, addr, ts))

	// Should not panic or error on underflow
	s.NoError(s.keeper.PruneExpiredNonces(earlyCtx))

	// Nonce should still exist (nothing is expired when cutoff = 0)
	has, _ := s.keeper.HasNonce(earlyCtx, addr, ts)
	s.True(has)
}

// --- ValidateGenesis Tests ---

func (s *KeeperTestSuite) TestValidateGenesis() {
	// Valid
	gs := types.DefaultGenesisState()
	s.NoError(types.ValidateGenesis(gs))

	// Duplicate entry
	gs.NonceEntries = []types.NonceEntry{
		{TimestampUs: 100, Address: "cosmos1abc"},
		{TimestampUs: 100, Address: "cosmos1abc"},
	}
	s.Error(types.ValidateGenesis(gs))

	// Empty address
	gs.NonceEntries = []types.NonceEntry{
		{TimestampUs: 100, Address: ""},
	}
	s.Error(types.ValidateGenesis(gs))

	// Zero timestamp
	gs.NonceEntries = []types.NonceEntry{
		{TimestampUs: 0, Address: "cosmos1abc"},
	}
	s.Error(types.ValidateGenesis(gs))

	// Invalid params
	gs = types.DefaultGenesisState()
	gs.Params.TimestampNonceCutoff = 0
	s.Error(types.ValidateGenesis(gs))
}

// --- Edge Case: Zero timestamp at genesis block ---

func (s *KeeperTestSuite) TestValidateTimestampNonce_GenesisBlockTime() {
	// Block time = 0 (genesis). Lower bound = max(0, 0 - window) = 0.
	genesisCtx := s.ctx.WithBlockTime(time.Unix(0, 0))
	addr := s.addrs[0]

	// Nonce = 0 should pass bounds check (>= lowerBound=0, <= upperBound=futureWindow).
	err := s.keeper.ValidateAndConsumeTimestampNonce(genesisCtx, addr, 0)
	s.NoError(err)

	// Duplicate at nonce=0
	err = s.keeper.ValidateAndConsumeTimestampNonce(genesisCtx, addr, 0)
	s.ErrorIs(err, types.ErrNonceDuplicate)

	// A nonce within future window should also work
	err = s.keeper.ValidateAndConsumeTimestampNonce(genesisCtx, addr, types.DefaultFutureWindowUs)
	s.NoError(err)

	// A nonce beyond future window should fail
	err = s.keeper.ValidateAndConsumeTimestampNonce(genesisCtx, addr, types.DefaultFutureWindowUs+1)
	s.ErrorIs(err, types.ErrNonceTooFarInFuture)
}

// --- Edge Case: uint64 overflow on upper bound ---

func (s *KeeperTestSuite) TestValidateTimestampNonce_UpperBoundOverflow() {
	// Set block time very large so that blockTimeUs + futureWindowUs overflows uint64.
	// time.Unix max safe is ~year 2262 for UnixMicro to fit in int64.
	// We use ValidateAndConsumeWithParams directly to control params.
	addr := s.addrs[0]

	// Near-max block time that fits in int64 microseconds: use 2200-01-01
	farFutureCtx := s.ctx.WithBlockTime(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	blockTimeUs := uint64(farFutureCtx.BlockTime().UnixMicro())

	// Set future window so large it would overflow
	overflowParams := types.Params{
		PastWindowUs:         types.DefaultPastWindowUs,
		FutureWindowUs:       math.MaxUint64 - blockTimeUs + 1, // exactly causes overflow
		TimestampNonceCutoff: types.DefaultTimestampNonceCutoff,
	}
	s.NoError(s.keeper.SetParams(farFutureCtx, overflowParams))

	// upperBound overflows -- should return an error rather than silently wrapping.
	err := s.keeper.ValidateAndConsumeTimestampNonce(farFutureCtx, addr, blockTimeUs)
	s.ErrorIs(err, types.ErrNonceOverflow)
}

// --- Edge Case: Future boundary exactness ---

func (s *KeeperTestSuite) TestValidateTimestampNonce_FutureBoundaryExact() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Exactly at upper bound: should succeed
	exactUpper := blockTimeUs + types.DefaultFutureWindowUs
	err := s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, exactUpper)
	s.NoError(err, "nonce at exact upper bound should be accepted")

	// One past upper bound: should fail
	addr2 := s.addrs[1]
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr2, exactUpper+1)
	s.ErrorIs(err, types.ErrNonceTooFarInFuture, "nonce one past upper bound should be rejected")
}

// --- Edge Case: ValidateAndConsumeWithParams matches ValidateAndConsumeTimestampNonce ---

func (s *KeeperTestSuite) TestValidateAndConsumeWithParams() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]
	params, err := s.keeper.GetParams(s.ctx)
	s.NoError(err)

	err = s.keeper.ValidateAndConsumeWithParams(s.ctx, addr, blockTimeUs, params)
	s.NoError(err)

	// Verify it's consumed
	has, err := s.keeper.HasNonce(s.ctx, addr, blockTimeUs)
	s.NoError(err)
	s.True(has)

	// Duplicate via WithParams
	err = s.keeper.ValidateAndConsumeWithParams(s.ctx, addr, blockTimeUs, params)
	s.ErrorIs(err, types.ErrNonceDuplicate)
}

// --- Prune: large number of expired entries ---

func (s *KeeperTestSuite) TestPruneLargeBatch() {
	addr := s.addrs[0]
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())

	// Create 1100 expired nonces to verify bulk pruning works.
	const n = 1100
	expiredBase := blockTimeUs - types.DefaultPastWindowUs - 1_000_000
	for i := uint64(0); i < n; i++ {
		s.NoError(s.keeper.SetNonce(s.ctx, addr, expiredBase+i))
	}

	// One valid nonce
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))

	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))

	// All expired should be gone
	for i := uint64(0); i < n; i++ {
		has, _ := s.keeper.HasNonce(s.ctx, addr, expiredBase+i)
		s.False(has, "expired nonce %d should be pruned", i)
	}

	// Valid should remain
	has, _ := s.keeper.HasNonce(s.ctx, addr, blockTimeUs)
	s.True(has, "valid nonce should remain")
}

// --- Pagination test for NoncesByAddress ---

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress_Paginated() {
	q := keeper.Querier{Keeper: s.keeper}
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Create 5 nonces
	for i := uint64(0); i < 5; i++ {
		s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs+i))
	}

	// Page 1: limit 2
	resp, err := q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address:    addr.String(),
		Pagination: &query.PageRequest{Limit: 2},
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 2)
	s.NotNil(resp.Pagination)
	s.NotEmpty(resp.Pagination.NextKey, "should have next key when more results exist")

	// Page 2: use NextKey cursor from page 1
	resp, err = q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address:    addr.String(),
		Pagination: &query.PageRequest{Key: resp.Pagination.NextKey, Limit: 2},
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 2)
	s.NotNil(resp.Pagination)
	s.NotEmpty(resp.Pagination.NextKey)

	// Page 3: use NextKey cursor from page 2 (only 1 remaining)
	resp, err = q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address:    addr.String(),
		Pagination: &query.PageRequest{Key: resp.Pagination.NextKey, Limit: 2},
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 1)
	s.NotNil(resp.Pagination)

	// No pagination param: defaults to limit 100, should return all 5
	resp, err = q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: addr.String(),
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 5)
}

// --- ExportGenesis edge cases ---

func (s *KeeperTestSuite) TestExportGenesisEmptyState() {
	// ExportGenesis on a fresh store should return empty entries, not panic.
	gs := s.keeper.ExportGenesis(s.ctx)
	s.NotNil(gs)
	s.Empty(gs.NonceEntries)
	s.Equal(types.DefaultParams(), gs.Params)
}

// --- MsgUpdateParams governance test ---

func (s *KeeperTestSuite) TestMsgUpdateParams() {
	ms := keeper.NewMsgServerImpl(s.keeper)

	// Valid authority
	newParams := types.Params{
		PastWindowUs:         uint64(10 * time.Minute.Microseconds()),
		FutureWindowUs:       uint64(10 * time.Minute.Microseconds()),
		TimestampNonceCutoff: 1 << 42,
	}
	_, err := ms.UpdateParams(s.ctx, &types.MsgUpdateParams{
		Authority: "cosmos1authority",
		Params:    newParams,
	})
	s.NoError(err)

	got, err := s.keeper.GetParams(s.ctx)
	s.NoError(err)
	s.Equal(newParams, got)

	// Wrong authority
	_, err = ms.UpdateParams(s.ctx, &types.MsgUpdateParams{
		Authority: "cosmos1wrongauthority",
		Params:    types.DefaultParams(),
	})
	s.Error(err)
	s.ErrorContains(err, govtypes.ErrInvalidSigner.Error())

	// Invalid params (cutoff = 0)
	_, err = ms.UpdateParams(s.ctx, &types.MsgUpdateParams{
		Authority: "cosmos1authority",
		Params:    types.Params{TimestampNonceCutoff: 0},
	})
	s.Error(err)
}
