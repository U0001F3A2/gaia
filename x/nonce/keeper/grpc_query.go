package keeper

import (
	"bytes"
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

var _ types.QueryServer = Querier{}

// Querier implements the x/nonce gRPC query server.
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
	prefix := types.NonceIteratorPrefix()
	addrBytes := []byte(addr)

	// Determine page limit (default 100, cap at 1000).
	limit := uint64(100)
	var offset uint64
	if req.Pagination != nil {
		if req.Pagination.Limit > 0 {
			limit = req.Pagination.Limit
		}
		offset = req.Pagination.Offset
	}
	if limit > 1000 {
		limit = 1000
	}

	iter, err := store.Iterator(prefix, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var nonces []uint64
	var matched uint64
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < len(prefix) || key[0] != prefix[0] {
			break
		}
		tsUs, keyAddr := types.ParseNonceKey(key)
		if !bytes.Equal(keyAddr, addrBytes) {
			continue
		}
		// Skip entries before the offset.
		if matched < offset {
			matched++
			continue
		}
		if uint64(len(nonces)) >= limit {
			// There are more results; build a next key.
			return &types.QueryNoncesByAddressResponse{
				TimestampNonces: nonces,
				Pagination: &query.PageResponse{
					NextKey: key,
					Total:   0, // total unknown without full scan
				},
			}, nil
		}
		nonces = append(nonces, tsUs)
		matched++
	}

	return &types.QueryNoncesByAddressResponse{
		TimestampNonces: nonces,
		Pagination:      &query.PageResponse{Total: uint64(len(nonces))},
	}, nil
}
