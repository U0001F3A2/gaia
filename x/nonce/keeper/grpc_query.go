package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

var _ types.QueryServer = Querier{}

type Querier struct {
	Keeper *Keeper
}

func (q Querier) Params(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	params, err := q.Keeper.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	return &types.QueryParamsResponse{Params: params}, nil
}

func (q Querier) HasNonce(ctx context.Context, req *types.QueryHasNonceRequest) (*types.QueryHasNonceResponse, error) {
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, err
	}
	has, err := q.Keeper.HasNonce(ctx, addr, req.TimestampUs)
	if err != nil {
		return nil, err
	}
	return &types.QueryHasNonceResponse{HasNonce: has}, nil
}

func (q Querier) NoncesByAddress(ctx context.Context, req *types.QueryNoncesByAddressRequest) (*types.QueryNoncesByAddressResponse, error) {
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, err
	}

	store := q.Keeper.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.NonceIteratorPrefix(), nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var nonces []uint64
	prefix := types.NonceIteratorPrefix()
	addrBytes := []byte(addr)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < len(prefix) || key[0] != prefix[0] {
			break
		}
		tsUs, keyAddr := types.ParseNonceKey(key)
		if len(keyAddr) == len(addrBytes) && bytesEqual(keyAddr, addrBytes) {
			nonces = append(nonces, tsUs)
		}
	}

	return &types.QueryNoncesByAddressResponse{TimestampNonces: nonces}, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
