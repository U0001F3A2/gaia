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
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}
	s.NoError(s.keeper.SetParams(s.ctx, custom))

	got, err := s.keeper.GetParams(s.ctx)
	s.NoError(err)
	s.Equal(custom, got)
}

func (s *KeeperTestSuite) TestValidateAndConsumeTimestampNonce() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Stateful sequence: consume -> duplicate -> same-ts-different-addr
	s.NoError(s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, blockTimeUs))
	s.ErrorIs(s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, blockTimeUs), types.ErrNonceDuplicate)
	s.NoError(s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, s.addrs[1], blockTimeUs))

	// Independent boundary cases (each uses a unique nonce value, no state dependency)
	tests := []struct {
		name    string
		nonce   uint64
		wantErr error
	}{
		{"expired nonce rejected", blockTimeUs - types.DefaultPastWindowUs - 1, types.ErrNonceExpired},
		{"future nonce rejected", blockTimeUs + types.DefaultFutureWindowUs + 1, types.ErrNonceTooFarInFuture},
		{"exact lower bound accepted", blockTimeUs - types.DefaultPastWindowUs, nil},
		{"exact upper bound accepted", blockTimeUs + types.DefaultFutureWindowUs, nil},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			err := s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, tc.nonce)
			if tc.wantErr == nil {
				s.NoError(err)
			} else {
				s.ErrorIs(err, tc.wantErr)
			}
		})
	}
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

func (s *KeeperTestSuite) TestKeyOrderingIsTimestampFirst() {
	addr := s.addrs[0]
	key1 := types.BuildNonceKey(100, addr)
	key2 := types.BuildNonceKey(200, addr)
	key3 := types.BuildNonceKey(200, s.addrs[1])

	// key1 < key2 (earlier timestamp sorts first)
	s.True(string(key1) < string(key2))
	// key2 and key3 share timestamp prefix, differ by address
	s.Equal(key2[:types.NonceKeyMinLen], key3[:types.NonceKeyMinLen])
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
	validAddr := s.addrs[0].String()

	tests := []struct {
		name        string
		entries     []types.NonceEntry
		modParams   func(*types.Params)
		errContains string
	}{
		{"valid default genesis", nil, nil, ""},
		{"valid genesis with entries", []types.NonceEntry{
			{TimestampUs: 100, Address: validAddr},
			{TimestampUs: 200, Address: validAddr},
		}, nil, ""},
		{"duplicate entry", []types.NonceEntry{
			{TimestampUs: 100, Address: validAddr},
			{TimestampUs: 100, Address: validAddr},
		}, nil, "duplicate nonce entry"},
		{"empty address", []types.NonceEntry{
			{TimestampUs: 100, Address: ""},
		}, nil, "empty address"},
		{"invalid bech32 address", []types.NonceEntry{
			{TimestampUs: 100, Address: "cosmos1invalid"},
		}, nil, "invalid nonce entry address"},
		{"zero timestamp", []types.NonceEntry{
			{TimestampUs: 0, Address: validAddr},
		}, nil, "zero timestamp"},
		{"invalid params wrong cutoff", nil, func(p *types.Params) {
			p.TimestampNonceCutoff = 42
		}, "protocol constant"},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			gs := types.DefaultGenesisState()
			if tc.entries != nil {
				gs.NonceEntries = tc.entries
			}
			if tc.modParams != nil {
				tc.modParams(&gs.Params)
			}
			err := types.ValidateGenesis(gs)
			if tc.errContains == "" {
				s.NoError(err)
			} else {
				s.Error(err)
				s.Contains(err.Error(), tc.errContains)
			}
		})
	}
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

	// Construct params that would overflow (bypasses Validate via direct call).
	overflowParams := types.Params{
		PastWindowUs:         types.DefaultPastWindowUs,
		FutureWindowUs:       math.MaxUint64 - blockTimeUs + 1, // exactly causes overflow
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}

	// upperBound overflows -- should return an error rather than silently wrapping.
	err := s.keeper.ValidateAndConsumeWithParams(farFutureCtx, addr, blockTimeUs, overflowParams)
	s.ErrorIs(err, types.ErrNonceOverflow)
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

	tenMin := uint64(10 * time.Minute.Microseconds())
	tests := []struct {
		name        string
		authority   string
		params      types.Params
		errContains string
	}{
		{"valid params", "cosmos1authority", types.Params{
			PastWindowUs:         tenMin,
			FutureWindowUs:       tenMin,
			TimestampNonceCutoff: types.TimestampNonceCutoff,
		}, ""},
		{"wrong authority", "cosmos1wrongauthority", types.DefaultParams(),
			govtypes.ErrInvalidSigner.Error()},
		{"wrong cutoff", "cosmos1authority", types.Params{
			PastWindowUs:         types.DefaultPastWindowUs,
			FutureWindowUs:       types.DefaultFutureWindowUs,
			TimestampNonceCutoff: 1 << 42,
		}, "protocol constant"},
		{"past window too large", "cosmos1authority", types.Params{
			PastWindowUs:         types.MaxWindowUs + 1,
			FutureWindowUs:       types.DefaultFutureWindowUs,
			TimestampNonceCutoff: types.TimestampNonceCutoff,
		}, "exceeds max"},
		{"future window too large", "cosmos1authority", types.Params{
			PastWindowUs:         types.DefaultPastWindowUs,
			FutureWindowUs:       types.MaxWindowUs + 1,
			TimestampNonceCutoff: types.TimestampNonceCutoff,
		}, "exceeds max"},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			_, err := ms.UpdateParams(s.ctx, &types.MsgUpdateParams{
				Authority: tc.authority,
				Params:    tc.params,
			})
			if tc.errContains == "" {
				s.NoError(err)
				got, err := s.keeper.GetParams(s.ctx)
				s.NoError(err)
				s.Equal(tc.params, got)
			} else {
				s.Error(err)
				s.ErrorContains(err, tc.errContains)
			}
		})
	}
}

// --- H5: Params validation edge cases ---

func (s *KeeperTestSuite) TestParamsValidate() {
	tests := []struct {
		name    string
		params  types.Params
		wantErr string
	}{
		{
			name:   "valid defaults",
			params: types.DefaultParams(),
		},
		{
			name: "zero past window",
			params: types.Params{
				PastWindowUs:         0,
				FutureWindowUs:       types.DefaultFutureWindowUs,
				TimestampNonceCutoff: types.TimestampNonceCutoff,
			},
			wantErr: "past_window_us below minimum",
		},
		{
			name: "zero future window",
			params: types.Params{
				PastWindowUs:         types.DefaultPastWindowUs,
				FutureWindowUs:       0,
				TimestampNonceCutoff: types.TimestampNonceCutoff,
			},
			wantErr: "future_window_us below minimum",
		},
		{
			name: "past window exceeds max",
			params: types.Params{
				PastWindowUs:         types.MaxWindowUs + 1,
				FutureWindowUs:       types.DefaultFutureWindowUs,
				TimestampNonceCutoff: types.TimestampNonceCutoff,
			},
			wantErr: "exceeds max",
		},
		{
			name: "future window exceeds max",
			params: types.Params{
				PastWindowUs:         types.DefaultPastWindowUs,
				FutureWindowUs:       types.MaxWindowUs + 1,
				TimestampNonceCutoff: types.TimestampNonceCutoff,
			},
			wantErr: "exceeds max",
		},
		{
			name: "wrong cutoff",
			params: types.Params{
				PastWindowUs:         types.DefaultPastWindowUs,
				FutureWindowUs:       types.DefaultFutureWindowUs,
				TimestampNonceCutoff: 42,
			},
			wantErr: "protocol constant",
		},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			err := tc.params.Validate()
			if tc.wantErr == "" {
				s.NoError(err)
			} else {
				s.Error(err)
				s.Contains(err.Error(), tc.wantErr)
			}
		})
	}
}

// --- H4: MsgUpdateParams.ValidateBasic() ---

func (s *KeeperTestSuite) TestMsgUpdateParamsValidateBasic() {
	validAuthority := s.addrs[0].String()

	tests := []struct {
		name        string
		authority   string
		params      types.Params
		errContains string
	}{
		{"valid message", validAuthority, types.DefaultParams(), ""},
		{"invalid bech32 authority", "invalid-authority", types.DefaultParams(), "decoding bech32 failed"},
		{"invalid params", validAuthority, types.Params{
			PastWindowUs:         0,
			FutureWindowUs:       types.DefaultFutureWindowUs,
			TimestampNonceCutoff: types.TimestampNonceCutoff,
		}, "past_window_us below minimum"},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			msg := &types.MsgUpdateParams{Authority: tc.authority, Params: tc.params}
			err := msg.ValidateBasic()
			if tc.errContains == "" {
				s.NoError(err)
			} else {
				s.Error(err)
				s.Contains(err.Error(), tc.errContains)
			}
		})
	}
}

// --- H7+M7: GRPC query edge cases ---

func (s *KeeperTestSuite) TestGRPCQueryHasNonce_NilRequest() {
	q := keeper.Querier{Keeper: s.keeper}
	_, err := q.HasNonce(s.ctx, nil)
	s.Error(err)
}

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress_NilRequest() {
	q := keeper.Querier{Keeper: s.keeper}
	_, err := q.NoncesByAddress(s.ctx, nil)
	s.Error(err)
}

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress_InvalidAddress() {
	q := keeper.Querier{Keeper: s.keeper}
	_, err := q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: "invalid-bech32",
	})
	s.Error(err)
}

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress_FiltersByAddress() {
	q := keeper.Querier{Keeper: s.keeper}

	// Use addr0 as target, write nonces under a different address to force scanning.
	// We write just over the typical page limit to verify scanning terminates.
	addr0 := s.addrs[0]
	addr1 := s.addrs[1]
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())

	// Write 5 nonces for addr1 (fills scan space) and 1 for addr0
	for i := uint64(0); i < 5; i++ {
		s.NoError(s.keeper.SetNonce(s.ctx, addr1, blockTimeUs+i))
	}
	s.NoError(s.keeper.SetNonce(s.ctx, addr0, blockTimeUs))

	resp, err := q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address:    addr0.String(),
		Pagination: &query.PageRequest{Limit: 100},
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 1)
	s.Equal(blockTimeUs, resp.TimestampNonces[0])
}

// --- L5: NoncesByAddress ordering verification ---

func (s *KeeperTestSuite) TestGRPCQueryNoncesByAddress_Ordering() {
	q := keeper.Querier{Keeper: s.keeper}
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Insert nonces out of order
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs+2000))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs+1000))

	resp, err := q.NoncesByAddress(s.ctx, &types.QueryNoncesByAddressRequest{
		Address: addr.String(),
	})
	s.NoError(err)
	s.Len(resp.TimestampNonces, 3)

	// Keys are (prefix + timestamp_be + addr), so iteration should yield ascending timestamps
	s.Equal(blockTimeUs, resp.TimestampNonces[0], "first nonce should be smallest timestamp")
	s.Equal(blockTimeUs+1000, resp.TimestampNonces[1])
	s.Equal(blockTimeUs+2000, resp.TimestampNonces[2], "last nonce should be largest timestamp")
}

// --- Prune watermark tests ---

func (s *KeeperTestSuite) TestReplayAfterWindowExpansion() {
	addr := s.addrs[0]
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())

	// 1. Consume a nonce 4 minutes in the past (within default 5 min window)
	fourMinAgo := blockTimeUs - 4*60*1_000_000
	s.NoError(s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, fourMinAgo))

	// 2. Advance time by 6 minutes so the nonce is now outside the 5 min window
	advancedCtx := s.ctx.WithBlockTime(s.ctx.BlockTime().Add(6 * time.Minute))
	s.NoError(s.keeper.PruneExpiredNonces(advancedCtx))

	// Verify nonce was pruned
	has, _ := s.keeper.HasNonce(advancedCtx, addr, fourMinAgo)
	s.False(has, "nonce should be pruned")

	// 3. Expand past_window_us from 5 min to 15 min via governance
	expandedParams := types.Params{
		PastWindowUs:         15 * 60 * 1_000_000,
		FutureWindowUs:       types.DefaultFutureWindowUs,
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}
	s.NoError(s.keeper.SetParams(advancedCtx, expandedParams))

	// 4. Try the same nonce: it's within the expanded window but the watermark blocks it
	err := s.keeper.ValidateAndConsumeTimestampNonce(advancedCtx, addr, fourMinAgo)
	s.ErrorIs(err, types.ErrNonceExpired,
		"replay should be blocked by prune watermark even with expanded window")
}

func (s *KeeperTestSuite) TestPruneWatermarkMonotonic() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())

	// First prune sets watermark
	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))
	wm1, err := s.keeper.GetPruneWatermark(s.ctx)
	s.NoError(err)
	expectedCutoff := blockTimeUs - types.DefaultPastWindowUs
	s.Equal(expectedCutoff, wm1)

	// Shrink past window (smaller window -> larger cutoff)
	smallerWindow := types.Params{
		PastWindowUs:         1 * 60 * 1_000_000, // 1 min
		FutureWindowUs:       types.DefaultFutureWindowUs,
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}
	s.NoError(s.keeper.SetParams(s.ctx, smallerWindow))
	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))
	wm2, err := s.keeper.GetPruneWatermark(s.ctx)
	s.NoError(err)
	s.True(wm2 >= wm1, "watermark must not decrease")

	// Expand past window (larger window -> smaller cutoff, but watermark stays)
	largerWindow := types.Params{
		PastWindowUs:         15 * 60 * 1_000_000, // 15 min
		FutureWindowUs:       types.DefaultFutureWindowUs,
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}
	s.NoError(s.keeper.SetParams(s.ctx, largerWindow))
	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))
	wm3, err := s.keeper.GetPruneWatermark(s.ctx)
	s.NoError(err)
	s.Equal(wm2, wm3, "watermark must not decrease when window expands")
}

func (s *KeeperTestSuite) TestGenesisRoundTripWithWatermark() {
	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	addr := s.addrs[0]

	// Set a nonce and prune to establish watermark
	s.NoError(s.keeper.SetNonce(s.ctx, addr, blockTimeUs))
	s.NoError(s.keeper.PruneExpiredNonces(s.ctx))

	wm, err := s.keeper.GetPruneWatermark(s.ctx)
	s.NoError(err)
	s.True(wm > 0)

	// Export
	gs := s.keeper.ExportGenesis(s.ctx)
	s.Equal(wm, gs.PruneHighWatermarkUs)

	// Import into fresh keeper
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_wm"))
	ctx2 := testCtx.Ctx.WithBlockTime(s.ctx.BlockTime())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	storeService := runtime.NewKVStoreService(key)
	k2 := keeper.NewKeeper(cdc, storeService, "cosmos1authority")
	k2.InitGenesis(ctx2, gs)

	wm2, err := k2.GetPruneWatermark(ctx2)
	s.NoError(err)
	s.Equal(wm, wm2, "watermark should survive genesis round-trip")
}

func (s *KeeperTestSuite) TestPruneWatermarkEnforcedInValidation() {
	addr := s.addrs[0]

	// Manually set a high watermark (simulating past pruning)
	watermark := uint64(s.ctx.BlockTime().UnixMicro()) - 1*60*1_000_000 // 1 min ago
	s.NoError(s.keeper.SetPruneWatermark(s.ctx, watermark))

	// Try a nonce that's within the default 5 min window but below the watermark
	belowWatermark := watermark - 1
	err := s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, belowWatermark)
	s.ErrorIs(err, types.ErrNonceExpired,
		"nonce below watermark should be rejected even if within time window")

	// Nonce at exactly the watermark should also be rejected (< lowerBound after max)
	// Wait -- watermark IS the lower bound. nonce == lowerBound should pass (>= check)
	// Actually, the lowerBound check is `nonceUs < lowerBound`, so nonce == watermark passes.
	err = s.keeper.ValidateAndConsumeTimestampNonce(s.ctx, addr, watermark)
	s.NoError(err, "nonce at exact watermark should be accepted")
}
